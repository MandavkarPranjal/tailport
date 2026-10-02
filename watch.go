package main

import (
	"context"
	"fmt"
	"slices"

	"github.com/MandavkarPranjal/tailport/internal/node"
	"github.com/MandavkarPranjal/tailport/internal/reqlog"
	"github.com/MandavkarPranjal/tailport/internal/state"
	"github.com/MandavkarPranjal/tailport/internal/ui"
)

// historyLines is how many past requests watch shows before it starts streaming
// new ones. It fills a dashboard screen, and on a terminal-wide log stream it is
// enough context to see what a share is doing without scrolling for ever.
const historyLines = 20

// watch shows the requests a share is serving, live. The serving process writes
// them to a log next to its state record, so watching works just as well for a
// background `tailport up -d` as for one running in another terminal.
func watch(ctx context.Context, args []string, env *ui.Env) error {
	fs := newFlagSet("watch", env, usage)
	stateDir := fs.String("state-dir", "", "where tailport keeps its records")
	lines := fs.Int("lines", historyLines, "how many past requests to show before the live feed")
	// bindShort can only alias a flag that already exists, so it has to come
	// after the flag itself.
	bindShort(fs, "n", "lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := *stateDir
	if dir == "" {
		dir = node.StateDir()
	}

	run, err := watchedRun(dir, fs.Args(), env)
	if err != nil {
		return err
	}
	if run == nil {
		return nil
	}
	if !state.Alive(run.PID) {
		// The log is gone on a clean exit, so anything still readable belongs to a
		// run that is over. Say so rather than let the dashboard look alive.
		env.Hint("Port %d has exited; showing the requests it recorded.", run.Port)
	}

	path := reqlog.Path(dir, run.PID)
	history, err := reqlog.ReadAll(path)
	if err != nil {
		return err
	}
	if n := *lines; n >= 0 && len(history) > n {
		history = history[len(history)-n:]
	}
	// Start the tail only after the history is read. The tail picks up where the
	// file already ends, so this ordering shows every request exactly once.
	tail := reqlog.Start(path, reqlog.Options{})
	go tail.Follow(ctx)

	return env.Dashboard(ctx, watchTitle(run), history, tail.Events(), tail.Dropped)
}

// watchedRun works out which share to watch: the port the user named, the only
// run there is, or one the user picks from a menu. A nil run means there is
// nothing to watch, which is already reported.
func watchedRun(dir string, args []string, env *ui.Env) (*state.Run, error) {
	if len(args) > 1 {
		return nil, usageError("unexpected argument %q", args[1])
	}
	if len(args) == 1 {
		port, err := parsePort(args[0])
		if err != nil {
			return nil, usageError("%q is not a port number", args[0])
		}
		return namedRun(dir, port, env)
	}

	runs, err := state.List(dir)
	if err != nil {
		return nil, err
	}
	switch len(runs) {
	case 0:
		env.Title("Nothing shared")
		env.Detail("Start with: %s", env.Cyan("tailport up -p 3000"))
		return nil, nil
	case 1:
		return &runs[0], nil
	}
	// Several shares at once, so ask. The pid carries the choice back, since two
	// runs can never agree on it the way ports can.
	items := make([]ui.Choice, 0, len(runs))
	for _, run := range runs {
		items = append(items, ui.Choice{
			Label: fmt.Sprint(run.Port),
			Note:  run.Hostname,
			Value: fmt.Sprint(run.PID),
		})
	}
	picked, err := env.Choose("Which share should tailport watch?", items, 0)
	if err != nil {
		return nil, err
	}
	// An empty answer means nothing was chosen, which is a way of saying no.
	if picked == "" {
		return nil, nil
	}
	if i := slices.IndexFunc(runs, func(run state.Run) bool {
		return fmt.Sprint(run.PID) == picked
	}); i >= 0 {
		return &runs[i], nil
	}
	return nil, nil
}

// namedRun finds the run sharing one port.
func namedRun(dir string, port int, env *ui.Env) (*state.Run, error) {
	runs, err := state.List(dir)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		if run.Port == port {
			return &run, nil
		}
	}
	env.Notice("Port %d is not shared.", port)
	env.Detail("See what is with: %s", env.Cyan("tailport status"))
	return nil, nil
}

// watchTitle is the heading of the dashboard: enough to tell shares apart when
// several are running, and no more.
func watchTitle(run *state.Run) string {
	return fmt.Sprintf("port %d  %s", run.Port, run.Hostname)
}
