package main

import (
	"testing"

	builderv0 "github.com/codefly-dev/core/generated/go/codefly/services/builder/v0"
)

func TestNodeAuditOptionsPreserveRequestedDependencyScope(t *testing.T) {
	options := nodeAuditOptions(&builderv0.AuditRequest{
		IncludeOutdated:        true,
		IncludeDevDependencies: true,
	})
	if !options.IncludeOutdated || !options.IncludeDevDependencies {
		t.Fatalf("node audit options = %+v", options)
	}

	runtimeOnly := nodeAuditOptions(&builderv0.AuditRequest{})
	if runtimeOnly.IncludeOutdated || runtimeOnly.IncludeDevDependencies {
		t.Fatalf("runtime-only audit options = %+v", runtimeOnly)
	}
}
