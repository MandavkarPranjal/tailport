// Package daemon runs a tailport in the background by re-executing the same
// binary with a hidden subcommand.
package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// DetachedEnv marks the re-executed child so it knows to detach and to log to
// a file rather than the terminal it was started from.
const DetachedEnv = "TAILPORT_DETACHED"

// ServeCommand is the hidden subcommand that runs one tunnel.
const ServeCommand = "__serve"

// Options describes the tunnel a detached tailport should run.
type Options struct {
	// Port is the local port to share.
	Port int
	// Path is the URL prefix to publish under.
	Path string
	// Hostname is the tailport node name.
	Hostname string
	// StateDir holds node credentials and run records.
	StateDir string
	// LogPath is where the detached run writes its output.
	LogPath string
	// All publishes every port Funnel supports.
	All bool
	// Private keeps the run inside the tailnet instead of publishing it.
	Private bool
	// Ephemeral removes the node from the tailnet when the run ends.
	Ephemeral bool
	// AuthKey logs the node in without a browser.
	AuthKey string
}

// Args renders the options as the child command's arguments, so the parent and
// the child cannot disagree about what to serve.
func (o Options) Args() []string {
	args := []string{
		ServeCommand,
		"-port", strconv.Itoa(o.Port),
		"-hostname", o.Hostname,
		"-state-dir", o.StateDir,
	}
	if o.Path != "" {
		args = append(args, "-path", o.Path)
	}
	if o.LogPath != "" {
		args = append(args, "-log", o.LogPath)
	}
	if o.All {
		args = append(args, "-all")
	}
	if o.Private {
		args = append(args, "-private")
	}
	if o.Ephemeral {
		args = append(args, "-ephemeral")
	}
	if o.AuthKey != "" {
		args = append(args, "-authkey", o.AuthKey)
	}
	return args
}

// Start re-executes the current binary in the background and returns the child's
// pid. Output goes to opts.LogPath.
func Start(opts Options) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("find tailport binary: %w", err)
	}
	logFile, err := os.OpenFile(opts.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, opts.Args()...)
	cmd.Env = append(os.Environ(), DetachedEnv+"=1")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start detached tailport: %w", err)
	}
	pid := cmd.Process.Pid
	// Release the child so it is reparented to init and keeps running.
	if err := cmd.Process.Release(); err != nil {
		return pid, nil
	}
	return pid, nil
}

// IsDetached reports whether this process is the re-executed child.
func IsDetached() bool {
	return os.Getenv(DetachedEnv) != ""
}

// LogFileName is the log path a detached run uses inside the state directory.
func LogFileName(stateDir, hostname string, port int) string {
	name := hostname
	if name == "" {
		name = "tailport"
	}
	return filepath.Join(stateDir, "logs", fmt.Sprintf("%s-%d.log", name, port))
}

// WaitForFile blocks until path exists or ctx is done. It is used to wait for a
// detached run to publish its state file before reporting success.
func WaitForFile(ctx context.Context, path string) error {
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// pollInterval is how often WaitForFile looks for the file.
const pollInterval = 200 * time.Millisecond
