//go:build !unix

package state

import "os"

// Alive reports whether a process with pid is still running. On Windows
// os.FindProcess opens the process, so it only succeeds for a live pid, and
// there is no signal 0 to send.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}

// Signal asks a running tailport to shut down.
func Signal(pid int, _ os.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
