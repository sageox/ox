//go:build windows

package adapters

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// configureOneShotCommand gives the direct adapter process its own console
// process group. Descendant lifetime is enforced by runOneShotCommand's Job
// Object; the group also keeps console control events scoped away from ox.
func configureOneShotCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED,
	}
}

// runOneShotCommand assigns the adapter to a kill-on-close Job Object. Windows
// Job membership is inherited by descendants, so closing the job after a
// timeout, output-limit cancellation, or ordinary adapter exit terminates the
// entire subprocess tree rather than only the direct adapter process.
func runOneShotCommand(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create adapter job object: %w", err)
	}
	defer windows.CloseHandle(job) //nolint:errcheck // closing is the kill-on-close action

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return fmt.Errorf("configure adapter job object: %w", err)
	}

	configureOneShotCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}

	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		killAndWait(cmd)
		return fmt.Errorf("open adapter process for job assignment: %w", err)
	}
	defer windows.CloseHandle(process) //nolint:errcheck // process exit owns lifecycle
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		killAndWait(cmd)
		return fmt.Errorf("assign adapter process to job object: %w", err)
	}
	if err := resumeProcess(uint32(cmd.Process.Pid)); err != nil {
		killAndWait(cmd)
		return fmt.Errorf("resume adapter process after job assignment: %w", err)
	}

	return cmd.Wait()
}

func resumeProcess(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot) //nolint:errcheck // snapshot is read-only

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err := windows.Thread32First(snapshot, &entry); err != nil {
		return err
	}
	for {
		if entry.OwnerProcessID == processID {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return err
			}
			defer windows.CloseHandle(thread) //nolint:errcheck // resumed thread owns lifecycle
			_, err = windows.ResumeThread(thread)
			return err
		}
		if err := windows.Thread32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				return fmt.Errorf("primary thread not found for process %d", processID)
			}
			return err
		}
	}
}

func killAndWait(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
