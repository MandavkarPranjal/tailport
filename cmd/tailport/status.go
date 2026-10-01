package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/node"
	"github.com/MandavkarPranjal/tailport/internal/state"
	"github.com/MandavkarPranjal/tailport/internal/ui"
)

// status shows what tailport is sharing right now, reading the run records the
// serving processes leave behind.
func status(ctx context.Context, args []string, env *ui.Env) error {
	fs := newFlagSet("status", env, usage)
	stateDir := fs.String("state-dir", "", "where tailport keeps its records")
	all := fs.Bool("all", false, "include records whose process has exited")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usageError("unexpected argument %q", rest[0])
	}
	dir := *stateDir
	if dir == "" {
		dir = node.StateDir()
	}

	if !*all {
		if _, err := state.Prune(dir); err != nil {
			return err
		}
	}
	runs, err := state.List(dir)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		env.Title("Nothing shared")
		env.Detail("Start with: %s", env.Cyan("tailport"))
		return nil
	}

	env.Title("Shared by tailport")
	for _, run := range runs {
		printRun(env, run)
	}
	return nil
}

// printRun reports one run: what it serves, where, and for how long.
func printRun(env *ui.Env, run state.Run) {
	scope := "public web"
	if run.Mode == state.ModeTailnet {
		scope = "tailnet only"
	}
	env.Line("")
	env.Line("%s  port %d  %s", env.Bold(run.Hostname), run.Port, env.Dim(scope))
	for _, url := range run.URLs {
		env.URL(url)
	}
	env.Detail("pid %d, since %s", run.PID, run.Started.Local().Format("2006-01-02 15:04:05"))
	if !state.Alive(run.PID) {
		env.Detail("This process is gone; the record is stale.")
	}
	if run.LogPath != "" {
		env.Detail("Log: %s", run.LogPath)
	}
}

// stop ends sharing. With no port it stops every tailport on this machine.
func stop(ctx context.Context, args []string, env *ui.Env) error {
	fs := newFlagSet("stop", env, usage)
	stateDir := fs.String("state-dir", "", "where tailport keeps its records")
	timeout := fs.Duration("timeout", 10*time.Second, "how long to wait for a clean exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := *stateDir
	if dir == "" {
		dir = node.StateDir()
	}

	runs, err := state.List(dir)
	if err != nil {
		return err
	}

	var wanted []string
	for _, arg := range fs.Args() {
		port, err := parsePort(arg)
		if err != nil {
			return usageError("%q is not a port number", arg)
		}
		wanted = append(wanted, fmt.Sprint(port))
	}

	targets := runs
	if len(wanted) > 0 {
		targets = nil
		for _, port := range wanted {
			found := false
			for _, run := range runs {
				if fmt.Sprint(run.Port) == port {
					targets = append(targets, run)
					found = true
				}
			}
			if !found {
				env.Notice("Port %s is not shared.", port)
			}
		}
	}
	if len(targets) == 0 {
		if len(runs) == 0 {
			env.Line("Nothing is shared.")
		}
		return nil
	}

	for _, run := range targets {
		if err := stopRun(dir, run, *timeout); err != nil {
			env.Errorf("stop port %d: %v", run.Port, err)
			continue
		}
		env.Ok("Stopped sharing port %d (%s)", run.Port, run.Hostname)
	}
	return nil
}

// stopRun asks a run to shut down, waits for it, and removes its record.
func stopRun(stateDir string, run state.Run, timeout time.Duration) error {
	if !state.Alive(run.PID) {
		return state.Remove(stateDir, run.PID)
	}
	if err := state.Signal(run.PID, os.Interrupt); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for state.Alive(run.PID) {
		if time.Now().After(deadline) {
			return fmt.Errorf("pid %d did not exit within %s", run.PID, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return state.Remove(stateDir, run.PID)
}
