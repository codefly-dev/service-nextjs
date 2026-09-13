//go:build ciinputs_conformance

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codefly-dev/core/ciinputs"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/codefly-dev/core/resources"
	"github.com/stretchr/testify/require"
)

func TestEffectiveInputsNativeNextAndVitest(t *testing.T) {
	service, client := inputsFixture(t)
	root := service.Location
	source := filepath.Join(root, "code")
	declaration, err := resources.LoadServiceFromDir(context.Background(), root)
	require.NoError(t, err)
	declaration.Spec["docker-image"] = "node:22-alpine"
	require.NoError(t, declaration.SaveAtDir(context.Background(), root))
	inputWrite(t, root, "code/package.json", `{"private":true,"scripts":{"test":"vitest run","test:pure":"vitest run","build":"next build","typecheck":"tsc --noEmit"},"dependencies":{"next":"16.2.12","react":"19.2.8","react-dom":"19.2.8"},"devDependencies":{"vitest":"3.2.7","typescript":"5.9.3","@types/node":"22.19.15","@types/react":"19.2.14"}}`)
	inputWrite(t, root, "code/app/layout.js", `export default function Layout({children}) { return <html><body>{children}</body></html> }`)
	inputWrite(t, root, "code/app/page.js", `import {value} from '../production.test.js'; export default function Page() { return <p>{value}</p> }`)
	inputWrite(t, root, "code/production.test.js", `export const value = 'production'`)
	inputWrite(t, root, "code/next.config.js", `const fs=require('node:fs');fs.readFileSync('config-fixture.txt');module.exports={}`)
	inputWrite(t, root, "code/config-fixture.txt", `configuration input`)
	inputWrite(t, root, "code/tsconfig.json", `{"compilerOptions":{"noEmit":true,"skipLibCheck":true},"include":["compiler.ts"]}`)
	inputWrite(t, root, "code/compiler.ts", `export const value: number = 1`)
	inputWrite(t, root, "code/vitest.config.mjs", `import {defineConfig} from 'vitest/config';export default defineConfig({test:{include:['checks/*.spec.js']}})`)
	inputWrite(t, root, "code/checks/value.spec.js", `import {test,expect} from 'vitest';import fs from 'node:fs';test('fixture',()=>expect(fs.readFileSync('fixture.txt','utf8')).toBe('ok'))`)
	inputWrite(t, root, "code/fixture.txt", `ok`)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("docker", append([]string{"run", "--rm", "--mount", "type=bind,src=" + source + ",dst=/app", "-w", "/app", "node:22-alpine"}, args...)...)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
	}
	run("npm", "install", "--no-audit", "--no-fund")
	project, err := service.inputProject(context.Background())
	require.NoError(t, err)
	native := discoverNativeInputs(context.Background(), project)
	require.True(t, native.Available, "isolated native discovery must succeed")
	require.True(t, native.CleanupVerified, "discovery containers must be confirmed removed")
	require.Contains(t, native.Tasks["TASK_PHASE_ARTIFACT_BUILD"], "code/production.test.js")
	require.NotContains(t, native.Tasks["TASK_PHASE_ARTIFACT_BUILD"], "code/checks/value.spec.js")
	require.Contains(t, native.Tasks["test/unit"], "code/checks/value.spec.js")
	require.Contains(t, native.Tasks["TASK_PHASE_COMPILE"], "code/compiler.ts")
	require.Contains(t, native.Tasks["TASK_PHASE_COMPILE"], "code/production.test.js")
	require.NotContains(t, native.Tasks["TASK_PHASE_ARTIFACT_BUILD"], "code/compiler.ts")
	// A fixture this size fits the path budget, so nothing is reported partial.
	require.False(t, native.Incomplete["TASK_PHASE_ARTIFACT_BUILD"])
	require.False(t, native.Incomplete["TASK_PHASE_COMPILE"])
	run("npm", "test", "--", "--maxWorkers=1")
	run("npm", "run", "build", "--", "--webpack")
	req := &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "native"}
	response, err := client.GetEffectiveInputs(context.Background(), req)
	require.NoError(t, err)
	artifact := inputTask(t, response, agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD, "")
	require.True(t, pathInput(t, artifact, "code/production.test.js").Sensitive)
	for _, in := range artifact.Inputs {
		require.NotEqual(t, "code/checks/value.spec.js", in.Name)
	}
	required, err := ciinputs.Required(nextValidationCapabilities())
	require.NoError(t, err)
	evaluated, err := ciinputs.Evaluate(response, req, required)
	require.NoError(t, err)
	for _, task := range evaluated {
		require.True(t, task.Conservative)
		require.False(t, task.CacheEligible)
	}

	t.Setenv("REVIEW_HARMLESS_SENTINEL", "host-only-value")
	inputWrite(t, root, "code/next.config.js", `if(process.env.REVIEW_HARMLESS_SENTINEL)throw new Error('host environment forwarded');module.exports={}`)
	native = discoverNativeInputs(context.Background(), project)
	require.True(t, native.Available)
	require.Contains(t, native.Tasks["TASK_PHASE_ARTIFACT_BUILD"], "code/production.test.js")
	inputWrite(t, root, "code/next.config.js", `const fs=require('node:fs');fs.writeFileSync('../outside.txt','bad');fs.writeFileSync('fixture.txt','after');module.exports={}`)
	before, err := os.ReadFile(filepath.Join(source, "fixture.txt"))
	require.NoError(t, err)
	response, err = client.GetEffectiveInputs(context.Background(), &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "hostile-config"})
	require.NoError(t, err)
	after, err := os.ReadFile(filepath.Join(source, "fixture.txt"))
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.NoFileExists(t, filepath.Join(root, "outside.txt"))
	for _, in := range inputTask(t, response, agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD, "").Inputs {
		require.NotEqual(t, "code/fixture.txt", in.Name)
	}

	// An application larger than the path budget must still observe a bounded
	// prefix and say the observation is partial. Discarding the whole list
	// instead would be indistinguishable from having observed nothing, which is
	// what an ordinary application used to get.
	inputWrite(t, root, "code/next.config.js", `module.exports={}`)
	var barrel strings.Builder
	for i := 0; i < 600; i++ {
		inputWrite(t, root, fmt.Sprintf("code/wide/module%04d.js", i), fmt.Sprintf("export const value%04d = %d;\n", i, i))
		barrel.WriteString(fmt.Sprintf("export {value%04d} from './module%04d';\n", i, i))
	}
	inputWrite(t, root, "code/wide/index.js", barrel.String())
	inputWrite(t, root, "code/app/wide/page.js", `import * as wide from '../../wide/index.js'; export default function Page() { return <p>{Object.keys(wide).length}</p> }`)
	native = discoverNativeInputs(context.Background(), project)
	require.True(t, native.Available)
	require.True(t, native.Observed["TASK_PHASE_ARTIFACT_BUILD"], "a large application must still be observed")
	require.True(t, native.Incomplete["TASK_PHASE_ARTIFACT_BUILD"], "a truncated observation must be declared partial")
	paths := native.Tasks["TASK_PHASE_ARTIFACT_BUILD"]
	require.NotEmpty(t, paths, "truncation must keep a prefix, not discard the observation")
	require.LessOrEqual(t, len(paths), nativePathBudget)
	response, err = client.GetEffectiveInputs(context.Background(), &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: "wide"})
	require.NoError(t, err)
	require.True(t, hasMarker(inputTask(t, response, agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD, ""), markerObservationIncomplete))
}
