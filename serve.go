package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/daemon"
	"github.com/MandavkarPranjal/tailport/internal/node"
	"github.com/MandavkarPranjal/tailport/internal/proxy"
	"github.com/MandavkarPranjal/tailport/internal/state"
	"github.com/MandavkarPranjal/tailport/internal/ui"
)

// startupTimeout is how long a detached run gets to publish its state file
// before `tailport up -d` gives up waiting and points at `tailport status`.
const startupTimeout = 90 * time.Second

// tailnetPort is the tailnet port tailport listens on, which is what makes the
// short URL http://tailport work.
const tailnetPort = 80

// serve is the hidden subcommand a detached tailport runs. It is also usable on
// its own when someone wants to supervise the tunnel themselves.
func serve(ctx context.Context, args []string, env *ui.Env, start node.Starter) error {
	fs := newFlagSet("serve", env, usage)
	var opts shareOpts
	fs.IntVar(&opts.Port, "port", 0, "local port to share")
	fs.BoolVar(&opts.All, "all", false, "publish on every port Funnel supports")
	fs.IntVar(&opts.PublicPort, "public-port", node.DefaultPublicPort, "public port to publish on")
	fs.StringVar(&opts.Path, "path", "", "publish under this URL prefix")
	fs.BoolVar(&opts.Private, "private", false, "share inside the tailnet only")
	fs.StringVar(&opts.LogPath, "log", "", "write output here instead of stdout")
	addCommon(fs, &opts.commonFlags)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usageError("unexpected argument %q", rest[0])
	}
	if opts.Port == 0 {
		return usageError("serve needs -p PORT")
	}
	if err := opts.validate(); err != nil {
		return err
	}
	return serveWith(ctx, env, opts, os.Getpid(), opts.LogPath, start)
}

// serveWith starts the node, opens the listeners, records what it is sharing and
// serves until ctx ends.
func serveWith(ctx context.Context, env *ui.Env, opts shareOpts, pid int, logPath string, start node.Starter) error {
	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = node.StateDir()
	}

	logs := log.New(env.Err, "", log.LstdFlags)
	if logPath != "" {
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		defer file.Close()
		logs = log.New(file, "", log.LstdFlags)
	}

	n, err := start(ctx, opts.nodeConfig(func(format string, args ...any) {
		logs.Printf(format, args...)
	}), func(url string) {
		env.Notice("log in to your tailnet to finish setting up %s", node.Name)
		env.URL(url)
	})
	if err != nil {
		return err
	}
	defer n.Close()

	handler := proxy.Mount(proxy.Loopback(opts.Port), opts.Path, logs)
	servers := make([]*http.Server, 0, 4)
	listeners := make([]net.Listener, 0, 4)

	open := func(ln net.Listener, srv *http.Server) error {
		listeners = append(listeners, ln)
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
				logs.Printf("serve: %v", err)
			}
		}()
		return nil
	}

	ln, err := n.ListenTailnet(tailnetPort)
	if err != nil {
		return err
	}
	if err := open(ln, proxy.Server(handler)); err != nil {
		return err
	}

	urls := []string{n.TailnetURL()}
	if !opts.Private {
		for _, public := range opts.publicPorts() {
			fln, err := n.ListenFunnel(public)
			if err != nil {
				return err
			}
			if err := open(fln, proxy.Server(handler)); err != nil {
				return err
			}
			urls = append(urls, n.PublicURL(public))
		}
	}

	mode := state.ModeFunnel
	if opts.Private {
		mode = state.ModeTailnet
	}
	hostname := n.Hostname()
	run := state.Run{
		PID:      pid,
		Port:     opts.Port,
		Path:     opts.Path,
		Hostname: hostname,
		Mode:     mode,
		Started:  time.Now(),
		URLs:     urls,
		LogPath:  logPath,
	}
	if err := state.Save(stateDir, run); err != nil {
		return err
	}
	defer func() { _ = state.Remove(stateDir, pid) }()

	if !daemon.IsDetached() {
		printShared(env, run, false)
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdownCtx)
	}
	for _, l := range listeners {
		_ = l.Close()
	}
	if !daemon.IsDetached() {
		env.Line("")
		env.Detail("Stopped sharing port %d.", opts.Port)
	}
	return nil
}

// login brings the tailport node up and waits for it, so a first-time user can
// do the browser step separately from sharing anything.
func login(ctx context.Context, args []string, env *ui.Env, start node.Starter) error {
	fs := newFlagSet("login", env, usage)
	var common commonFlags
	addCommon(fs, &common)
	if err := fs.Parse(args); err != nil {
		return err
	}

	env.Title("Logging the %s node in", node.Name)
	n, err := start(ctx, common.nodeConfig(func(format string, args ...any) {
		env.Detail(format, args...)
	}), func(url string) {
		env.Notice("Open this link to authorise %s:", node.Name)
		env.URL(url)
	})
	if err != nil {
		return err
	}
	defer n.Close()

	env.Ok("Logged in as %s", n.Hostname())
	if dns := n.DNSName(); dns != "" {
		env.Detail("Public address: https://%s", dns)
	} else {
		env.Detail("No MagicDNS name yet; check your tailnet DNS settings.")
	}
	return nil
}

// stateFilePath is where a run records itself, used to wait for a detached run.
func stateFilePath(stateDir string, pid int) string {
	return state.Dir(stateDir) + string(os.PathSeparator) + state.FileName(pid)
}

// loadRun reads one run's record.
func loadRun(stateDir string, pid int) (*state.Run, error) {
	return state.Load(stateDir, pid)
}

// printShared reports the addresses a run is reachable on.
func printShared(env *ui.Env, run state.Run, background bool) {
	scope := "the public web"
	if run.Mode == state.ModeTailnet {
		scope = "your tailnet only"
	}
	env.Title("Sharing http://127.0.0.1:%d%s on %s", run.Port, run.Path, scope)
	for _, url := range run.URLs {
		env.URL(url)
	}
	env.Line("")
	if run.Mode == state.ModeFunnel {
		env.Detail("Anyone on the internet who has the link can reach this service.")
		env.Detail("Serve a login page, or use --private to keep it inside your tailnet.")
	} else {
		env.Detail("Only devices in your tailnet can reach these addresses.")
	}
	if background {
		env.Detail("Keeps running in the background. Stop it with: tailport stop")
	} else {
		env.Detail("Press Ctrl-C to stop sharing.")
	}
}
