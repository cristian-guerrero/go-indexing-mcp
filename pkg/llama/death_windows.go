//go:build windows

package llama

import (
	"log/slog"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setChildDeath is a no-op on Windows; child death is handled via Job Object after Start().
func setChildDeath(cmd *exec.Cmd) {
	// On Windows, the child process death setup is done
	// after Start() via assignChildToJob + Job Object.
}

// setupJob creates a Windows Job Object with KILL_ON_JOB_CLOSE flag.
// When the parent process exits, Windows automatically terminates all processes
// assigned to this job, preventing orphaned llama-server processes.
func (m *Manager) setupJob() {
	if m.jobHandle != 0 {
		return
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		slog.Warn("failed to create job object, child may survive parent death", "error", err)
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	infoPtr := uintptr(unsafe.Pointer(&info))
	if _, err := windows.SetInformationJobObject(h,
		windows.JobObjectExtendedLimitInformation,
		infoPtr,
		uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		slog.Warn("failed to set kill-on-job-close, child may survive parent death", "error", err)
		return
	}
	if err := windows.AssignProcessToJobObject(h, windows.CurrentProcess()); err != nil {
		slog.Debug("current process already in a job, child assignment may fail", "error", err)
	}
	m.jobHandle = uintptr(h)
}

// assignChildToJob assigns the llama-server child process to the Job Object,
// ensuring it is killed when the parent (this process) exits.
func (m *Manager) assignChildToJob(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || m.jobHandle == 0 {
		return nil
	}
	child, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(child)

	job := windows.Handle(m.jobHandle)
	return windows.AssignProcessToJobObject(job, child)
}

// cleanupJob closes the Job Object handle to release Windows resources.
func (m *Manager) cleanupJob() {
	if m.jobHandle != 0 {
		windows.CloseHandle(windows.Handle(m.jobHandle))
		m.jobHandle = 0
	}
}
