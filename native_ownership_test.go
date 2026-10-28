package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/codefly-dev/core/agents/manager"
	runtimev0 "github.com/codefly-dev/core/generated/go/codefly/services/runtime/v0"
	"github.com/codefly-dev/core/network"
	"github.com/codefly-dev/core/resources"
	runners "github.com/codefly-dev/core/runners/base"
	"github.com/stretchr/testify/require"
)

// This opt-in gate consumes the actual `agent build --native-only` output.
// It never builds a substitute agent, touches the user's registry, or selects
// processes by name. Both services and their agent owners are disposable.
func TestNativeAgentOwnershipLifecycle(t *testing.T) {
	agentHome := os.Getenv("SERVICE_NEXTJS_NATIVE_AGENT_HOME")
	cli := os.Getenv("SERVICE_NEXTJS_QUALIFICATION_CLI")
	if agentHome == "" && cli == "" {
		t.Skip("set SERVICE_NEXTJS_NATIVE_AGENT_HOME and SERVICE_NEXTJS_QUALIFICATION_CLI to qualify built artifacts")
	}
	if !filepath.IsAbs(agentHome) || !filepath.IsAbs(cli) {
		t.Fatal("qualification inputs must be absolute paths")
	}
	// Next's disabled telemetry still starts a short-lived detached flush
	// helper at shutdown. It can race filesystem cleanup after the tracked
	// npm group is dead. Retry deletion of our fixture home only; this is not
	// permission to discover or signal processes outside that group.
	home, err := os.MkdirTemp("", "nextjs-native-home-")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.Eventually(t, func() bool { return os.RemoveAll(home) == nil }, 5*time.Second, 25*time.Millisecond)
	})
	t.Setenv("HOME", home) // Core's process registry is OS-home scoped.
	t.Setenv(resources.CodeflyHomeEnv, agentHome)
	t.Setenv("GOWORK", "off")
	installed, err := agent.Path(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{installed, installed + ".cdx.json", cli} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("artifact %s sha256=%x", path, sha256.Sum256(body))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	a := startOwnedNativeService(t, ctx)
	b := startOwnedNativeService(t, ctx)
	require.NotEqual(t, a.conn.ProcessInfo().PID, b.conn.ProcessInfo().PID, "sibling must have an independent agent owner")
	siblingBefore, err := b.group.InspectOwnership(ctx)
	require.NoError(t, err)
	assertNativeOwnership(t, ctx, a.group, a.workspace, true)
	assertNativeOwnership(t, ctx, b.group, b.workspace, true)

	// Ordinary Runtime.Stop must retire A without affecting B's exact birth.
	stopped, err := a.client.Stop(ctx, &runtimev0.StopRequest{})
	if err != nil || stopped.GetStatus().GetState() != runtimev0.StopStatus_SUCCESS {
		t.Fatalf("ordinary stop: %v, %v", stopped, err)
	}
	waitNativeCondition(t, func() bool { return !a.group.Alive() })
	if _, err := runners.LookupProcessGroup(a.group.PGID()); !errors.Is(err, runners.ErrProcessGroupNotRegistered) {
		t.Fatalf("ordinary stop retained the workload registration: %v", err)
	}
	assertNativeOwnership(t, ctx, b.group, b.workspace, true)
	t.Log("ordinary Stop removed first workload; sibling remains authenticated and supervised")

	// Restart through the same Runtime, then kill only its disposable owner
	// birth so no agent Stop/finalizer can mask recovery of an orphan workload.
	a.group = startNativeWorkload(t, ctx, a.client, a.workspace, a.address)
	owner, err := runners.InspectProcess(a.conn.ProcessInfo().PID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runners.SignalProcess(ctx, owner, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitNativeCondition(t, func() bool {
		ownership, err := a.group.InspectOwnership(ctx)
		return err == nil && ownership.Authenticated && !ownership.OwnerAlive
	})
	assertNativeOwnership(t, ctx, a.group, a.workspace, false)
	assertNativeOwnership(t, ctx, b.group, b.workspace, true)

	// Use the maintained CLI consumer and Core's strict receipt parser; do not
	// invent a recovery selection from registry JSON or a list of descendants.
	pgid := strconv.Itoa(a.group.PGID())
	preview, err := exec.CommandContext(ctx, cli, "recover", "process-group", pgid, "--preview").Output()
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	expected, err := runners.ParseProcessGroupRecoveryExpectation(preview)
	if err != nil || expected.Namespace != "authenticated-v2" || expected.PGID != a.group.PGID() || expected.Owner.PID != owner.PID {
		t.Fatalf("actual agent did not write the expected v2 registration: %+v, %v", expected, err)
	}
	require.Equal(t, owner.BootID, expected.Owner.BootID)
	require.Equal(t, owner.StartID, expected.Owner.StartID)
	t.Logf("orphan preview: %s", preview)
	assertNativeOwnership(t, ctx, a.group, a.workspace, false)
	receipt := filepath.Join(t.TempDir(), "recovery.json")
	if err := os.WriteFile(receipt, preview, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := exec.CommandContext(ctx, cli, "recover", "process-group", pgid, "--expect", receipt).CombinedOutput()
	if err != nil {
		t.Fatalf("recover: %v\n%s", err, result)
	}
	if a.group.Alive() {
		t.Fatal("recovery left the selected workload alive")
	}
	if _, err := runners.LookupProcessGroup(a.group.PGID()); !errors.Is(err, runners.ErrProcessGroupNotRegistered) {
		t.Fatalf("recovery did not remove the exact registration: %v", err)
	}
	assertNativeOwnership(t, ctx, b.group, b.workspace, true)
	siblingAfter, err := b.group.InspectOwnership(ctx)
	require.NoError(t, err)
	require.Equal(t, siblingBefore.Members, siblingAfter.Members, "sibling process births changed during recovery")
	t.Logf("recovery result: %s; sibling preserved", result)
	stopped, err = b.client.Stop(ctx, &runtimev0.StopRequest{})
	if err != nil || stopped.GetStatus().GetState() != runtimev0.StopStatus_SUCCESS {
		t.Fatalf("sibling ordinary stop: %v, %v", stopped, err)
	}
	waitNativeCondition(t, func() bool { return !b.group.Alive() })
	_, err = runners.LookupProcessGroup(b.group.PGID())
	require.ErrorIs(t, err, runners.ErrProcessGroupNotRegistered)
}

type ownedNativeService struct {
	workspace string
	address   string
	conn      *manager.AgentConn
	client    runtimev0.RuntimeClient
	group     *runners.TrackedProcessGroup
}

func startOwnedNativeService(t *testing.T, ctx context.Context) *ownedNativeService {
	t.Helper()
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	identity, environment := testIdentity(t, workspace)
	serviceDir := filepath.Join(workspace, identity.RelativeToWorkspace)
	writeProductionTestFile(t, serviceDir, "service.codefly.yaml", "name: frontend\nversion: 0.0.0\nendpoints:\n  - name: http\n    api: http\n")
	// The shipped application owns these exact framework pins. Exercise real
	// Next.js and npm (including their child processes), not a stand-in runner.
	manifest, err := os.ReadFile("base/code/package.json")
	require.NoError(t, err)
	var base struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(manifest, &base))
	dependencies := map[string]string{}
	for _, name := range []string{"next", "react", "react-dom"} {
		dependencies[name] = base.Dependencies[name]
	}
	fixture, err := json.Marshal(map[string]any{
		"name": "native-ownership", "private": true,
		"scripts":      map[string]string{"dev": "next dev --hostname 127.0.0.1"},
		"dependencies": dependencies,
	})
	require.NoError(t, err)
	writeProductionTestFile(t, serviceDir, "code/package.json", string(fixture))
	writeProductionTestFile(t, serviceDir, "code/pages/index.js", "export default function Home() { return 'native ownership'; }\n")
	writeProductionTestFile(t, serviceDir, "code/pages/api/pid.js", "export default function handler(req, res) { res.json({pid: process.pid}); }\n")
	t.Setenv(runners.LaunchWorkspaceEnv, workspace)
	conn, err := manager.Load(ctx, agent, manager.WithoutSandbox(), manager.WithoutPrincipal(), manager.WithWorkDir(workspace))
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	client := runtimev0.NewRuntimeClient(conn.GRPCConn())
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = client.Stop(cleanup, &runtimev0.StopRequest{})
	})
	environmentProto, err := environment.Proto()
	require.NoError(t, err)
	loaded, err := client.Load(ctx, &runtimev0.LoadRequest{Identity: identity, Environment: environmentProto, DisableCatch: true})
	if err != nil || loaded.GetStatus().GetState() != runtimev0.LoadStatus_READY {
		t.Fatalf("load: %v, %v\n%s", loaded, err, conn.StderrTail())
	}
	composition, err := resources.LoadWorkspaceFromDir(ctx, workspace)
	require.NoError(t, err)
	ports, err := network.NewRuntimeManager(ctx, nil)
	require.NoError(t, err)
	ports.WithTemporaryPorts()
	mappings, err := ports.GenerateNetworkMappings(ctx, environment, composition, resources.ServiceIdentityFromProto(identity), loaded.GetEndpoints(), resources.NewRuntimeContextNative())
	require.NoError(t, err)
	endpoint, err := resources.FindHTTPEndpoint(ctx, loaded.GetEndpoints())
	require.NoError(t, err)
	instance, err := resources.FindNetworkInstanceInNetworkMappings(ctx, mappings, endpoint, resources.NewNativeNetworkAccess())
	require.NoError(t, err)
	initialized, err := client.Init(ctx, &runtimev0.InitRequest{RuntimeContext: resources.NewRuntimeContextNative(), ProposedNetworkMappings: mappings})
	if err != nil || initialized.GetStatus().GetState() != runtimev0.InitStatus_READY {
		t.Fatalf("init: %v, %v\n%s", initialized, err, conn.StderrTail())
	}
	return &ownedNativeService{workspace: workspace, address: instance.Address, conn: conn, client: client,
		group: startNativeWorkload(t, ctx, client, workspace, instance.Address)}
}

func startNativeWorkload(t *testing.T, ctx context.Context, client runtimev0.RuntimeClient, workspace, address string) *runners.TrackedProcessGroup {
	t.Helper()
	// Returning from (and cancelling) Start's RPC must not end the workload.
	startCtx, cancel := context.WithCancel(ctx)
	started, err := client.Start(startCtx, &runtimev0.StartRequest{})
	cancel()
	if err != nil || started.GetStatus().GetState() != runtimev0.StartStatus_STARTED {
		t.Fatalf("start: %v, %v", started, err)
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	response, err := httpClient.Get(strings.TrimRight(address, "/") + "/api/pid")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var workload struct {
		PID int `json:"pid"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&workload))
	identity, err := runners.InspectProcess(workload.PID)
	require.NoError(t, err)
	group, err := runners.LookupProcessGroup(identity.PGID)
	require.NoError(t, err)
	t.Logf("registered leader %+v", group.Leader())
	assertNativeOwnership(t, ctx, group, workspace, true)
	ownership, err := group.InspectOwnership(ctx)
	require.NoError(t, err)
	require.Contains(t, ownership.Members, identity)
	t.Logf("Next.js HTTP process %+v in authenticated group %d", identity, group.PGID())
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = group.Terminate(cleanup)
		_ = group.RemoveIfDeadContext(cleanup)
	})
	return group
}

func assertNativeOwnership(t *testing.T, ctx context.Context, group *runners.TrackedProcessGroup, workspace string, ownerAlive bool) {
	t.Helper()
	ownership, err := group.InspectOwnership(ctx)
	if err != nil || !ownership.Authenticated || ownership.OwnerAlive != ownerAlive || ownership.LaunchWorkspace != workspace || len(ownership.Members) == 0 {
		t.Fatalf("ownership for %d (registered leader %+v): %+v, %v", group.PGID(), group.Leader(), ownership, err)
	}
}

func waitNativeCondition(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("native lifecycle postcondition did not arrive")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
