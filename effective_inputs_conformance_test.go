//go:build ciinputs_conformance

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/codefly-dev/core/ciinputs"
	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/stretchr/testify/require"
)

func TestEffectiveInputsNativeNextAndVitest(t *testing.T) {
	service, client := inputsFixture(t)
	root := service.Location
	source := filepath.Join(root, "code")
	inputWrite(t, root, "code/package.json", `{"private":true,"scripts":{"test":"vitest run","test:pure":"vitest run","build":"next build"},"dependencies":{"next":"16.2.12","react":"19.2.8","react-dom":"19.2.8"},"devDependencies":{"vitest":"3.2.7","typescript":"5.9.3"}}`)
	inputWrite(t, root, "code/app/layout.js", `export default function Layout({children}) { return <html><body>{children}</body></html> }`)
	inputWrite(t, root, "code/app/page.js", `import {value} from '../production.test.js'; export default function Page() { return <p>{value}</p> }`)
	inputWrite(t, root, "code/production.test.js", `export const value = 'production'`)
	inputWrite(t, root, "code/next.config.js", `const fs = require('node:fs'); fs.readFileSync('config-fixture.txt'); module.exports = {}`)
	inputWrite(t, root, "code/config-fixture.txt", `configuration input`)
	inputWrite(t, root, "code/vitest.config.mjs", `import {defineConfig} from 'vitest/config'; export default defineConfig({test:{include:['checks/*.spec.js']}})`)
	inputWrite(t, root, "code/checks/value.spec.js", `import {test,expect} from 'vitest'; import fs from 'node:fs'; test('fixture',()=>expect(fs.readFileSync('fixture.txt','utf8')).toBe('ok'))`)
	inputWrite(t, root, "code/fixture.txt", `ok`)
	install := exec.Command("npm", "install", "--no-audit", "--no-fund")
	install.Dir = source
	output, err := install.CombinedOutput()
	require.NoError(t, err, "%s", output)
	native, ok := discoverNativeInputs(context.Background(), source)
	require.True(t, ok, "native Next.js/Vitest discovery must succeed")
	require.Contains(t, native.Production, filepath.Join(source, "app/page.js"))
	require.Contains(t, native.Suites["unit"], filepath.Join(source, "checks/value.spec.js"))
	run := exec.Command("npm", "test", "--", "--maxWorkers=1")
	run.Dir = source
	output, err = run.CombinedOutput()
	require.NoError(t, err, "%s", output)
	required, err := ciinputs.Required(nextValidationCapabilities())
	require.NoError(t, err)
	discover := func(snapshot string) []ciinputs.Task {
		req := &agentv0.GetEffectiveInputsRequest{SchemaVersion: 1, Snapshot: snapshot}
		response, err := client.GetEffectiveInputs(context.Background(), req)
		require.NoError(t, err)
		artifact := inputTask(t, response, agentv0.TaskPhase_TASK_PHASE_ARTIFACT_BUILD, "")
		require.NotNil(t, pathInput(t, artifact, "code/production.test.js").Identity)
		require.NotNil(t, pathInput(t, artifact, "code/config-fixture.txt").Identity)
		tasks, err := ciinputs.Evaluate(response, req, required)
		require.NoError(t, err)
		return tasks
	}
	before := discover("before")
	inputWrite(t, root, "code/checks/value.spec.js", `import {test} from 'vitest'; test('changed',()=>{})`)
	after := discover("test-edit")
	require.Len(t, ciinputs.Changed(before, after), len(required), "dynamic consumption must not permit production exclusion")
	inputWrite(t, root, "code/.next/trace", `stale trace claims no inputs`)
	for _, task := range discover("stale-trace") {
		require.True(t, task.Conservative)
		require.False(t, task.CacheEligible)
	}
}
