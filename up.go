package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/MandavkarPranjal/tailport/internal/daemon"
	"github.com/MandavkarPranjal/tailport/internal/netports"
	"github.com/MandavkarPranjal/tailport/internal/node"
	"github.com/MandavkarPranjal/tailport/internal/ui"
)

// maxMenuRows bounds the port menu so a machine with a hundred listeners stays
// readable. The rest are reachable by typing a port.
const maxMenuRows = 20

// errNoPortSelected is returned when tailport has to guess a local port, which
// it refuses to do in scripts and would be useless in a menu anyway.
var errNoPortSelected = errors.New("no port selected: pass -p PORT (for example: tailport up -p 3000)")

// shareOpts is what `up` and the hidden `serve` subcommand agree on.
type shareOpts struct {
	commonFlags
	Port       int
	Path       string
	Private    bool
	All        bool
	PublicPort int
	AuthKey    string
	Yes        bool
	LogPath    string
}

func (o shareOpts) publicPorts() []int {
	if o.All {
		return node.PublicPorts
	}
	return []int{o.PublicPort}
}

func (o shareOpts) daemonOptions() daemon.Options {
	hostname := o.Hostname
	if hostname == "" {
		hostname = node.Hostname()
	}
	stateDir := o.StateDir
	if stateDir == "" {
		stateDir = node.StateDir()
	}
	logPath := o.LogPath
	if logPath == "" {
		logPath = daemon.LogFileName(stateDir, hostname, o.Port)
	}
	return daemon.Options{
		Port:      o.Port,
		Path:      o.Path,
		Hostname:  hostname,
		StateDir:  stateDir,
		LogPath:   logPath,
		All:       o.All,
		Private:   o.Private,
		Ephemeral: o.Ephemeral,
		AuthKey:   o.AuthKey,
	}
}

// up shares a port. With no flags it asks which one, which is the common case.
func up(ctx context.Context, args []string, env *ui.Env, start node.Starter) error {
	fs := newFlagSet("up", env, usage)
	var opts shareOpts
	fs.IntVar(&opts.Port, "port", 0, "local port to share")
	fs.BoolVar(&opts.All, "all", false, "publish on every port Funnel supports")
	fs.IntVar(&opts.PublicPort, "public-port", node.DefaultPublicPort, "public port to publish on")
	fs.StringVar(&opts.Path, "path", "", "publish under this URL prefix")
	fs.BoolVar(&opts.Private, "private", false, "share inside the tailnet only")
	fs.BoolVar(&opts.Yes, "yes", false, "never prompt")
	listOnly := fs.Bool("list", false, "list local listening ports and exit")
	daemonise := fs.Bool("daemon", false, "run in the background")
	addCommon(fs, &opts.commonFlags)
	bindShort(fs, "p", "port")
	bindShort(fs, "a", "all")
	bindShort(fs, "y", "yes")
	bindShort(fs, "d", "daemon")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usageError("unexpected argument %q", rest[0])
	}
	if *listOnly {
		return listPorts(env, nil)
	}

	if err := opts.validate(); err != nil {
		return err
	}

	port, err := resolvePort(env, opts.Port, opts.Yes)
	if err != nil {
		return err
	}
	opts.Port = port
	if !netports.IsListening(port) {
		env.Notice("nothing is listening on port %d yet", port)
	}

	if *daemonise {
		return startDaemon(ctx, env, opts)
	}
	return runShare(ctx, env, opts, start)
}

// validate rejects flag combinations that cannot both be honoured.
func (o shareOpts) validate() error {
	if o.Port < 0 || o.Port > 65535 {
		return usageError("--port %d is not a valid port", o.Port)
	}
	if o.PublicPort < 0 || o.PublicPort > 65535 {
		return usageError("--public-port %d is not a valid port", o.PublicPort)
	}
	if o.All && o.PublicPort != node.DefaultPublicPort {
		return usageError("--all already covers port %d; drop --public-port", o.PublicPort)
	}
	if !o.Private && !node.IsPublicPort(o.PublicPort) {
		return usageError("port %d cannot be published: use one of %s", o.PublicPort, node.PortsList())
	}
	if o.Path != "" && !strings.HasPrefix(o.Path, "/") {
		return usageError("--path must start with /")
	}
	return nil
}

// startDaemon re-runs tailport in the background and waits until the child has
// published its state file, so the URLs printed are real.
func startDaemon(ctx context.Context, env *ui.Env, opts shareOpts) error {
	dopts := opts.daemonOptions()
	pid, err := daemon.Start(dopts)
	if err != nil {
		return err
	}
	stateDir := dopts.StateDir
	waitCtx, cancel := context.WithTimeout(ctx, startupTimeout)
	defer cancel()

	statePath := stateFilePath(stateDir, pid)
	if err := daemon.WaitForFile(waitCtx, statePath); err != nil {
		env.Notice("tailport is still starting in the background (pid %d)", pid)
		env.Detail("Follow it with: tailport status")
		return nil
	}
	run, err := loadRun(stateDir, pid)
	if err != nil {
		return fmt.Errorf("read state of detached tailport: %w", err)
	}
	printShared(env, *run, true)
	env.Detail("Logs: %s", run.LogPath)
	return nil
}

// runShare serves in this process until ctx is cancelled or the user interrupts.
func runShare(ctx context.Context, env *ui.Env, opts shareOpts, start node.Starter) error {
	return serveWith(ctx, env, opts, os.Getpid(), "", start)
}

// resolvePort returns the local port to share. With no port and no way to ask,
// it errors rather than guessing.
func resolvePort(env *ui.Env, port int, yes bool) (int, error) {
	if port != 0 {
		return port, nil
	}
	if yes || !env.Interactive() {
		return 0, errNoPortSelected
	}

	listeners, err := netports.List()
	if err != nil || len(listeners) == 0 {
		if err != nil {
			env.Detail("no listening ports discovered (%v), so type one", err)
		}
		answer, askErr := env.Ask("Which local port should I share? ")
		if askErr != nil {
			return 0, errNoPortSelected
		}
		return parsePort(answer)
	}

	items := make([]ui.Choice, 0, maxMenuRows+1)
	for i, l := range listeners {
		if i >= maxMenuRows {
			break
		}
		items = append(items, ui.Choice{
			Label: strconv.Itoa(l.Port),
			Note:  l.String(),
			Value: strconv.Itoa(l.Port),
		})
	}
	items = append(items, ui.Choice{Label: "other", Note: "type a different port", Manual: true})

	answer, err := env.Choose("Which local port should I share?", items, 1)
	if err != nil {
		return 0, fmt.Errorf("%w (%w)", errNoPortSelected, err)
	}
	return parsePort(answer)
}

func parsePort(answer string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(answer))
	if err != nil {
		return 0, fmt.Errorf("%q is not a port number", answer)
	}
	return port, nil
}
