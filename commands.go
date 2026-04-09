package main

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	agentv0 "github.com/codefly-dev/core/generated/go/codefly/services/agent/v0"
	"github.com/codefly-dev/core/resources"
)

// registerCommands registers agent-specific commands.
// NOTE: test and lint are standard Runtime RPCs — don't duplicate here.
func (s *Runtime) registerCommands() {
	s.RegisterCommand(&agentv0.CommandDefinition{
		Name:        "screenshot",
		Description: "Take a screenshot of the running frontend (requires Playwright)",
		Usage:       `screenshot {"path": "output.png", "url": "/"}`,
		Tags:        []string{"ui", "testing", "visual"},
	}, s.cmdScreenshot)

	s.RegisterCommand(&agentv0.CommandDefinition{
		Name:        "health",
		Description: "Check if the Next.js frontend is responding",
		Tags:        []string{"health", "diagnostic"},
	}, s.cmdHealth)

	s.RegisterCommand(&agentv0.CommandDefinition{
		Name:        "routes",
		Description: "List all page routes in the Next.js app",
		Tags:        []string{"info", "routing"},
	}, s.cmdRoutes)

	s.RegisterCommand(&agentv0.CommandDefinition{
		Name:        "playwright",
		Description: "Run Playwright end-to-end tests",
		Usage:       `playwright {"target": "tests/e2e", "headed": false}`,
		Tags:        []string{"testing", "e2e", "browser"},
	}, s.cmdPlaywright)
}

func (s *Runtime) cmdScreenshot(ctx context.Context, args []string) (string, error) {
	if s.runner == nil {
		return "", fmt.Errorf("frontend is not running")
	}

	outputPath := "screenshot.png"
	targetURL := "/"
	for i, arg := range args {
		if arg == "--path" && i+1 < len(args) {
			outputPath = args[i+1]
		}
		if arg == "--url" && i+1 < len(args) {
			targetURL = args[i+1]
		}
	}

	addr, err := s.findHTTPAddress()
	if err != nil {
		return "", err
	}

	script := fmt.Sprintf(
		`const { chromium } = require('playwright');
		(async () => {
			const browser = await chromium.launch();
			const page = await browser.newPage();
			await page.goto('%s%s');
			await page.screenshot({ path: '%s', fullPage: true });
			await browser.close();
			console.log('Screenshot saved to %s');
		})();`, addr, targetURL, outputPath, outputPath)

	proc, err := s.nativeEnv.NewProcess("node", "-e", script)
	if err != nil {
		return "", fmt.Errorf("cannot create screenshot process: %w", err)
	}
	runErr := proc.Run(ctx)
	if runErr != nil {
		return "", fmt.Errorf("screenshot failed: %w", runErr)
	}
	return fmt.Sprintf("Screenshot saved to %s", outputPath), nil
}

func (s *Runtime) cmdHealth(_ context.Context, _ []string) (string, error) {
	if s.runner == nil {
		return "NOT RUNNING", nil
	}
	addr, err := s.findHTTPAddress()
	if err != nil {
		return "", err
	}
	resp, err := http.Get(addr)
	if err != nil {
		return fmt.Sprintf("UNHEALTHY: %v", err), nil
	}
	defer resp.Body.Close()
	return fmt.Sprintf("HEALTHY: HTTP %d", resp.StatusCode), nil
}

func (s *Runtime) cmdRoutes(ctx context.Context, _ []string) (string, error) {
	// Short-lived command — exec is fine for output capture
	cmd := exec.CommandContext(ctx, "find", "src/app", "-name", "page.tsx", "-o", "-name", "page.ts")
	cmd.Dir = s.sourceLocation
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cannot list routes: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var routes []string
	for _, line := range lines {
		route := strings.TrimPrefix(line, "src/app")
		route = strings.TrimSuffix(route, "/page.tsx")
		route = strings.TrimSuffix(route, "/page.ts")
		if route == "" {
			route = "/"
		}
		routes = append(routes, route)
	}
	return fmt.Sprintf("Routes (%d):\n%s", len(routes), strings.Join(routes, "\n")), nil
}

func (s *Runtime) cmdPlaywright(ctx context.Context, args []string) (string, error) {
	if s.runner == nil {
		return "", fmt.Errorf("frontend is not running — start it first")
	}

	addr, err := s.findHTTPAddress()
	if err != nil {
		return "", err
	}

	// Build npx playwright command
	pwArgs := []string{"playwright", "test"}
	headed := false
	for _, arg := range args {
		switch arg {
		case "--headed":
			headed = true
		default:
			pwArgs = append(pwArgs, arg)
		}
	}
	if headed {
		pwArgs = append(pwArgs, "--headed")
	}

	cmd := exec.CommandContext(ctx, "npx", pwArgs...)
	cmd.Dir = s.sourceLocation
	cmd.Env = append(cmd.Env, fmt.Sprintf("BASE_URL=%s", addr))
	cmd.Env = append(cmd.Env, fmt.Sprintf("PLAYWRIGHT_BASE_URL=%s", addr))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("playwright tests failed: %w", err)
	}
	return string(output), nil
}

func (s *Runtime) findHTTPAddress() (string, error) {
	net, err := resources.FindNetworkInstanceInNetworkMappings(
		context.Background(), s.NetworkMappings, s.HttpEndpoint, resources.NewNativeNetworkAccess())
	if err != nil {
		return "", fmt.Errorf("cannot find HTTP address: %w", err)
	}
	return fmt.Sprintf("http://%s", net.Address), nil
}
