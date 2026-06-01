package llama

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewLock(t *testing.T) {
	l := NewLock()
	if l == nil {
		t.Fatal("expected non-nil lock")
	}
	if l.ourID != os.Getpid() {
		t.Fatalf("expected ourID=%d, got %d", os.Getpid(), l.ourID)
	}
}

func TestLock_StartAndRelease(t *testing.T) {
	dir := t.TempDir()
	l := &Lock{path: filepath.Join(dir, "llama-server.lock"), ourID: os.Getpid()}

	if err := l.Start(56000); err != nil {
		t.Fatal(err)
	}

	data, err := l.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if data == nil {
		t.Fatal("expected lock data")
	}
	if data.Port != 56000 {
		t.Fatalf("expected port 56000, got %d", data.Port)
	}

	released, err := l.Release()
	if err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("expected released=true after single owner")
	}
}

func TestLock_AddPIDAndRelease(t *testing.T) {
	dir := t.TempDir()
	l := &Lock{path: filepath.Join(dir, "llama-server.lock"), ourID: 100}

	if err := l.Start(56001); err != nil {
		t.Fatal(err)
	}

	l2 := &Lock{path: l.path, ourID: 200}
	if err := l2.AddPID(); err != nil {
		t.Fatal(err)
	}

	pids, err := l2.Peek()
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 2 {
		t.Fatalf("expected 2 PIDs, got %d", len(pids))
	}

	// Release first owner
	released, err := l.Release()
	if err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("expected released=false while other PIDs remain")
	}

	// Release second owner
	released, err = l2.Release()
	if err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("expected released=true after all PIDs released")
	}
}

func TestLock_ReleaseNonexistent(t *testing.T) {
	dir := t.TempDir()
	l := &Lock{path: filepath.Join(dir, "nonexistent.lock"), ourID: os.Getpid()}

	released, err := l.Release()
	if err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("expected released=true for nonexistent lock")
	}
}

func TestLock_AcquireNonexistent(t *testing.T) {
	dir := t.TempDir()
	l := &Lock{path: filepath.Join(dir, "nonexistent.lock"), ourID: os.Getpid()}

	data, err := l.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if data != nil {
		t.Fatal("expected nil for nonexistent lock")
	}
}

func TestLock_AddPIDCreatesLockIfMissing(t *testing.T) {
	dir := t.TempDir()
	l := &Lock{path: filepath.Join(dir, "new.lock"), ourID: os.Getpid()}

	if err := l.AddPID(); err != nil {
		t.Fatal(err)
	}

	pids, err := l.Peek()
	if err != nil {
		t.Fatal(err)
	}
	if len(pids) != 1 || pids[0] != os.Getpid() {
		t.Fatalf("expected our PID, got %v", pids)
	}
}

func TestLock_AddPIDIdempotent(t *testing.T) {
	dir := t.TempDir()
	l := &Lock{path: filepath.Join(dir, "lock.json"), ourID: 300}

	if err := l.Start(56002); err != nil {
		t.Fatal(err)
	}

	// Adding same PID twice should be idempotent
	if err := l.AddPID(); err != nil {
		t.Fatal(err)
	}

	pids, _ := l.Peek()
	if len(pids) != 1 {
		t.Fatalf("expected 1 PID (idempotent AddPID), got %d", len(pids))
	}
}

func TestLock_ForceClear(t *testing.T) {
	dir := t.TempDir()
	l := &Lock{path: filepath.Join(dir, "lock.json"), ourID: os.Getpid()}

	l.Start(56003)

	if err := l.ForceClear(); err != nil {
		t.Fatal(err)
	}

	data, _ := l.Acquire()
	if data != nil {
		t.Fatal("expected nil after force clear")
	}
}

func TestLock_ForceClearNonexistent(t *testing.T) {
	l := &Lock{path: filepath.Join(t.TempDir(), "nonexistent.lock"), ourID: os.Getpid()}

	if err := l.ForceClear(); err != nil {
		t.Fatal("expected no error clearing nonexistent lock")
	}
}

func TestLock_PeekNonexistent(t *testing.T) {
	dir := t.TempDir()
	l := &Lock{path: filepath.Join(dir, "nonexistent.lock"), ourID: os.Getpid()}

	pids, err := l.Peek()
	if err == nil {
		t.Fatal("expected error for nonexistent lock")
	}
	if pids != nil {
		t.Fatal("expected nil PIDs")
	}
}

func TestLock_LockPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")
	l := &Lock{path: path, ourID: os.Getpid()}

	if l.LockPath() != path {
		t.Fatalf("expected %q, got %q", path, l.LockPath())
	}
}
