//go:build linux

package provider

import (
	"context"
	"os"
	"testing"
)

// TestBubblewrapIntegration runs a real provider inside bwrap with network
// denied and rlimits applied through the exec wrapper. Skips when the host
// cannot create the namespaces (CI installs bubblewrap to exercise it).
func TestBubblewrapIntegration(t *testing.T) {
	if resolveBubblewrap("deny") == "" {
		t.Skip("bubblewrap unavailable or namespaces blocked")
	}
	s := helperSpec(t, "items")
	s.Sandbox = SandboxPolicy{Mode: "required", Network: "deny", Limits: ResourceLimits{CPUSeconds: 30, OpenFiles: 256}}
	r := Run(context.Background(), s, req(t.TempDir()), "x")
	if !r.OK() || !r.Sandboxed || r.SandboxBackend != "bubblewrap" || !r.LimitsEnforced || len(r.Items) == 0 {
		t.Fatalf("r=%+v", r)
	}
	if os.Getenv("CI") != "" {
		t.Logf("bubblewrap sandbox exercised: %+v", r.SandboxNetwork)
	}
}
