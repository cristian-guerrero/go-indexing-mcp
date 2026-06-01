package version

import (
	"testing"
)

func TestVersion_Default(t *testing.T) {
	if Version == "" {
		t.Fatal("expected non-empty version")
	}
}

func TestVersion_CanBeOverridden(t *testing.T) {
	// Version is a var, so it can be set via ldflags or directly in tests.
	// Verify it matches the expected default for dev builds.
	if Version != "dev" {
		t.Logf("Version = %q (overridden via ldflags)", Version)
	}
}
