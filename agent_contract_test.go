package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/codefly-dev/core/agents"
	"github.com/codefly-dev/core/agents/contract"
	agentv1 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v1"
	"github.com/codefly-dev/core/runners/recoveryscope"
)

// The SDK fills an absent contract on its way out of the server, so only a
// declaration read straight from the handler proves this agent made it.
func TestAgentDeclaresItsOwnLifecycleContract(t *testing.T) {
	info, err := NewService().GetAgentInformation(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, contract.Check(info.GetContract(), contract.ContainerRecoveryScope))
}

func TestBuiltAgentIsAdmittedOverAuthenticatedDiscovery(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "service-nextjs")
	build, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	require.NoError(t, err, "%s", build)

	scope, namespace := strings.Repeat("a", 64), strings.Repeat("b", 64)
	command := exec.Command(binary)
	command.Env = append(os.Environ(),
		"CODEFLY_AGENT_TOKEN=contract-test",
		"CODEFLY_AGENT_UDS_PATH=",
		recoveryscope.EnvironmentVariable+"="+recoveryscope.Marker(os.Getpid(), scope, namespace),
	)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	var stderr agentOutput
	command.Stderr = &stderr
	require.NoError(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })

	spoken, endpoint := readHandshake(t, stdout, &stderr)

	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, agents.AuthMetadataKey, "contract-test")
	var headers metadata.MD
	info, err := agentv1.NewAgentClient(conn).GetAgentInformation(
		ctx, &agentv1.AgentInformationRequest{}, grpc.Header(&headers))
	require.NoError(t, err, "agent stderr:\n%s", stderr.String())

	require.NoError(t, contract.Check(info.GetContract(), contract.ContainerRecoveryScope))
	require.Equal(t, spoken, info.GetContract().GetStartupProtocolVersion())
	require.Equal(t, []string{scope + ":" + namespace}, headers.Get(recoveryscope.Header))
}

// readHandshake returns the startup protocol version the agent process spoke
// and the endpoint it published, from the "VERSION|<endpoint>" first line.
func readHandshake(t *testing.T, stdout io.Reader, stderr *agentOutput) (uint32, string) {
	t.Helper()
	lines := make(chan string, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err != nil {
			return
		}
		lines <- line
	}()
	select {
	case line := <-lines:
		version, endpoint, ok := strings.Cut(strings.TrimSpace(line), "|")
		require.True(t, ok, "malformed handshake %q", line)
		spoken, err := strconv.ParseUint(version, 10, 32)
		require.NoError(t, err, "malformed handshake %q", line)
		return uint32(spoken), endpoint
	case <-time.After(30 * time.Second):
		t.Fatalf("agent did not emit a handshake\nagent stderr:\n%s", stderr.String())
		return 0, ""
	}
}

// agentOutput collects what the agent process writes to stderr. os/exec copies
// into it from its own goroutine for as long as the process runs, and the test
// reads it while the agent is still up, so both go through one lock.
type agentOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (o *agentOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.Write(p)
}

func (o *agentOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buffer.String()
}
