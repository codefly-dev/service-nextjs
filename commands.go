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

func (s *Runtime) registerCommands() {
	// screenshot: capture a screenshot of the running frontend
	s.RegisterCommand(&agentv0.CommandDefinition{
		Name:        "screenshot",
		Description: "Take a screenshot of the running frontend (requires Playwright)",
		Usage:       "screenshot [--path output.png] [--url /]",
		Tags:        []string{"ui", "testing", "visual"},
	}, s.cmdScreenshot)

	// health: check if the frontend is responding
	s.RegisterCommand(&agentv0.CommandDefinition{
		Name:        "health",
		Description: "Check if the Next.js frontend is responding",
		Tags:        []string{"health", "diagnostic"},
	}, s.cmdHealth)

	// routes: list all Next.js routes
	s.RegisterCommand(&agentv0.CommandDefinition{
		Name:        "routes",
		Description: "List all page routes in the Next.js app",
		Tags:        []string{"info", "routing"},
	}, s.cmdRoutes)

	// npm: run an npm script
	s.RegisterCommand(&agentv0.CommandDefinition{
		Name:        "npm",
		Description: "Run an npm script in the service directory",
		Usage:       "npm <script> [args...]",
		Tags:        []string{"build", "dev"},
	}, s.cmdNpm)
}

func (s *Runtime) cmdScreenshot(ctx context.Context, args []string) (string, error) {
	if s.runner == nil {
		return "", fmt.Errorf("frontend is not running")
	}

	// Check if playwright is available
	if _, err := exec.LookPath("npx"); err != nil {
		return "", fmt.Errorf("npx not found — install Node.js")
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

	// Get the running port from the endpoint
	port := s.HttpEndpoint.GetName()
	// TODO: get actual port from network mapping

	// Use playwright via npx to take screenshot
	script := fmt.Sprintf(
		`const { chromium } = require('playwright');
		(async () => {
			const browser = await chromium.launch();
			const page = await browser.newPage();
			await page.goto('http://localhost:%s%s');
			await page.screenshot({ path: '%s', fullPage: true });
			await browser.close();
			console.log('Screenshot saved to %s');
		})();`, port, targetURL, outputPath, outputPath)

	cmd := exec.CommandContext(ctx, "node", "-e", script)
	cmd.Dir = s.sourceLocation
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("screenshot failed: %w\n%s", err, string(output))
	}

	return fmt.Sprintf("Screenshot saved to %s", outputPath), nil
}

func (s *Runtime) cmdHealth(ctx context.Context, _ []string) (string, error) {
	if s.runner == nil {
		return "NOT RUNNING", nil
	}

	// Try to reach the frontend
	net, err := s.findHTTPAddress()
	if err != nil {
		return "", err
	}

	resp, err := http.Get(net)
	if err != nil {
		return fmt.Sprintf("UNHEALTHY: %v", err), nil
	}
	defer resp.Body.Close()

	return fmt.Sprintf("HEALTHY: HTTP %d", resp.StatusCode), nil
}

func (s *Runtime) cmdRoutes(ctx context.Context, _ []string) (string, error) {
	// Walk the app directory to find page files
	cmd := exec.CommandContext(ctx, "find", "src/app", "-name", "page.tsx", "-o", "-name", "page.ts")
	cmd.Dir = s.sourceLocation
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("cannot list routes: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var routes []string
	for _, line := range lines {
		// Convert file path to route: src/app/admin/users/page.tsx → /admin/users
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

func (s *Runtime) cmdNpm(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: npm <script> [args...]")
	}

	npmArgs := append([]string{"run"}, args...)
	cmd := exec.CommandContext(ctx, "npm", npmArgs...)
	cmd.Dir = s.sourceLocation
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("npm failed: %w\n%s", err, string(output))
	}

	return string(output), nil
}

func (s *Runtime) findHTTPAddress() (string, error) {
	// Find the HTTP network instance for health check
	net, err := resources.FindNetworkInstanceInNetworkMappings(
		context.Background(), s.NetworkMappings, s.HttpEndpoint, resources.NewNativeNetworkAccess())
	if err != nil {
		return "", fmt.Errorf("cannot find HTTP address: %w", err)
	}
	return fmt.Sprintf("http://%s", net.Address), nil
}
