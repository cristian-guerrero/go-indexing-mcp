package llama

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/cristian-guerrero/go-indexing-mcp/pkg/config"
)

// LockData represents the shared lock file content.
type LockData struct {
	Port int   `json:"port"`
	PIDs []int `json:"pids"`
}

// Lock manages a cross-process lock file at ~/.go-mcp/llama-server.lock.
// Multiple MCP processes coordinate via this file: the lock records which PIDs
// are currently using the shared llama-server, and what port it runs on.
// This prevents one MCP from killing the server while others still need it.
type Lock struct {
	path  string
	mu    sync.Mutex
	ourID int
}

// NewLock creates a lock manager for the current process.
func NewLock() *Lock {
	return &Lock{
		path:  filepath.Join(config.McpDir(), "llama-server.lock"),
		ourID: os.Getpid(),
	}
}

// Acquire reads the lock file and returns active lock data if valid PIDs exist.
// It cleans up stale PIDs (crashed processes) and removes the lock file if all
// are dead. Returns nil when no active lock is found (caller should start fresh).
func (l *Lock) Acquire() (*LockData, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	data, err := l.read()
	if err != nil {
		return nil, nil
	}

	alive := make([]int, 0, len(data.PIDs))
	for _, pid := range data.PIDs {
		if pid == l.ourID || isProcessAlive(pid) {
			alive = append(alive, pid)
		}
	}

	if len(alive) == 0 {
		os.Remove(l.path)
		return nil, nil
	}

	if len(alive) != len(data.PIDs) {
		data.PIDs = alive
		if err := l.write(data); err != nil {
			slog.Warn("lock: failed to save cleaned PIDs", "error", err)
		}
	}

	return data, nil
}

// Start creates or replaces the lock file with our PID as the sole owner.
// Used when we are starting a brand-new llama-server instance.
func (l *Lock) Start(port int) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	data := &LockData{
		Port: port,
		PIDs: []int{l.ourID},
	}
	return l.write(data)
}

// AddPID registers our PID in an existing lock file.
// Used when attaching to an already-running server started by another MCP.
func (l *Lock) AddPID() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	data, err := l.read()
	if err != nil {
		return l.write(&LockData{
			Port: 56000,
			PIDs: []int{l.ourID},
		})
	}

	for _, pid := range data.PIDs {
		if pid == l.ourID {
			return nil
		}
	}
	data.PIDs = append(data.PIDs, l.ourID)
	sort.Ints(data.PIDs)
	return l.write(data)
}

// Release removes our PID from the lock file.
// Returns true when the lock file is now empty (caller should stop the server).
func (l *Lock) Release() (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	data, err := l.read()
	if err != nil {
		return true, nil
	}

	remaining := make([]int, 0, len(data.PIDs))
	for _, pid := range data.PIDs {
		if pid != l.ourID {
			remaining = append(remaining, pid)
		}
	}

	if len(remaining) == 0 {
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			return true, fmt.Errorf("remove lock: %w", err)
		}
		return true, nil
	}

	data.PIDs = remaining
	if err := l.write(data); err != nil {
		return false, fmt.Errorf("save lock: %w", err)
	}
	return false, nil
}

// ForceClear removes the lock file unconditionally.
func (l *Lock) ForceClear() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (l *Lock) read() (*LockData, error) {
	f, err := os.ReadFile(l.path)
	if err != nil {
		return nil, err
	}
	var data LockData
	if err := json.Unmarshal(f, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (l *Lock) write(data *LockData) error {
	tmp := l.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, l.path)
}

// Peek returns the current PIDs from the lock file without modifying anything.
// Returns nil if the lock file doesn't exist or is unreadable.
func (l *Lock) Peek() ([]int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	data, err := l.read()
	if err != nil {
		return nil, err
	}
	pids := make([]int, len(data.PIDs))
	copy(pids, data.PIDs)
	return pids, nil
}

// LockPath returns the lock file path (useful for logging).
func (l *Lock) LockPath() string {
	return l.path
}
