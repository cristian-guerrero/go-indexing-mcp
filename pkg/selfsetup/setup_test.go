package selfsetup

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsInteractive_WithPipe(t *testing.T) {
	orig := os.Stdout
	defer func() { os.Stdout = orig }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	os.Stdout = w

	if isInteractive() {
		t.Error("expected isInteractive=false when stdout is a pipe")
	}
}

func TestCopySelf(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}

	// copySelf copies the test binary to McpBinDir
	if err := copySelf(); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(os.Getenv(homeEnvVar()), ".go-mcp", "indexing", "bin")
	name := "go-indexing-mcp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(binDir, name)

	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("expected binary at %s: %v", dest, err)
	}
}

func TestCopySelf_AlreadyInstalled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", t.TempDir())
	} else {
		t.Setenv("HOME", t.TempDir())
	}

	// First copy
	if err := copySelf(); err != nil {
		t.Fatal(err)
	}

	// Second copy should succeed (idempotent)
	if err := copySelf(); err != nil {
		t.Fatal(err)
	}
}

func TestAddToPATH_AlreadyInPath(t *testing.T) {
	// Set HOME/USERPROFILE so config.McpBinDir() points to a temp dir
	home := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	} else {
		t.Setenv("HOME", home)
	}

	binDir := filepath.Join(home, ".go-mcp", "indexing", "bin")
	t.Setenv("PATH", binDir)

	err := addToPATH()
	if err != nil {
		t.Fatalf("expected no error when already in PATH, got: %v", err)
	}
}

func homeEnvVar() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}
