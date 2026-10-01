// tailport shares a local port with your tailnet, and optionally the public
// internet, over its own embedded Tailscale node named "tailport".
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/MandavkarPranjal/tailport/internal/daemon"
	"github.com/MandavkarPranjal/tailport/internal/node"
	"github.com/MandavkarPranjal/tailport/internal/ui"
)

// version is stamped at build time with -ldflags "-X main.version=v1.2.3".
var version = "0.1.0"

func main() {
	env := ui.NewEnv(os.Stdin, os.Stdout, os.Stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], env); err != nil {
		env.Errorf("%v", err)
		os.Exit(1)
	}
}

// errUsage marks a mistake in the command line, so the caller prints usage too.
var errUsage = errors.New("usage")

func usageError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errUsage, fmt.Sprintf(format, args...))
}

// run dispatches one tailport invocation. It is separate from main so tests can
// drive the CLI with their own streams.
func run(ctx context.Context, args []string, env *ui.Env) error {
	cmd := "up"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "up":
		err = up(ctx, args, env)
	case daemon.ServeCommand:
		err = serve(ctx, args, env)
	case "status":
		err = status(ctx, args, env)
	case "stop":
		err = stop(ctx, args, env)
	case "list":
		err = listPorts(env, args)
	case "login":
		err = login(ctx, args, env)
	case "version":
		env.Line("tailport %s", version)
	case "help", "-h", "--help":
		usage(env.Out)
	default:
		err = usageError("unknown command %q", cmd)
	}

	if errors.Is(err, flag.ErrHelp) {
		// -h already printed the command's flags; the overview helps too.
		usage(env.Out)
		return nil
	}
	if err != nil && errors.Is(err, errUsage) {
		usage(env.Err)
	}
	return err
}

// newFlagSet returns a flag set that reports errors through env and never calls
// os.Exit on its own.
func newFlagSet(name string, env *ui.Env, help func(io.Writer)) *flag.FlagSet {
	fs := flag.NewFlagSet("tailport "+name, flag.ContinueOnError)
	fs.SetOutput(env.Err)
	fs.Usage = func() { help(env.Err) }
	return fs
}

// bindShort gives a flag a one-letter alias, so -p and --port are the same flag
// without listing each alias twice in help. An alias follows the flag it points
// at, so -a stays boolean and -p still takes a value.
func bindShort(fs *flag.FlagSet, short, long string) {
	target := fs.Lookup(long)
	if target == nil {
		return
	}
	alias := &flagAlias{target: target.Value}
	if bf, ok := target.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
		alias.bool = true
	}
	fs.Var(alias, short, "alias for --"+long)
}

// flagAlias forwards to another flag's value.
type flagAlias struct {
	target flag.Value
	bool   bool
}

func (a *flagAlias) Set(s string) error {
	if a.target == nil {
		return errUsage
	}
	return a.target.Set(s)
}

// IsBoolFlag lets a boolean flag be given as -a rather than -a=true.
func (a *flagAlias) IsBoolFlag() bool { return a.bool }

func (a *flagAlias) Get() any {
	if g, ok := a.target.(flag.Getter); ok {
		return g.Get()
	}
	return nil
}

func (a *flagAlias) String() string {
	if a.target == nil {
		return ""
	}
	return a.target.String()
}

// addCommon registers the flags every serving command shares.
func addCommon(fs *flag.FlagSet, opts *commonFlags) {
	fs.StringVar(&opts.Hostname, "hostname", "", "node name in your tailnet (default "+node.Name+")")
	fs.StringVar(&opts.StateDir, "state-dir", "", "where to keep node credentials (default "+node.StateDir()+")")
	fs.StringVar(&opts.AuthKey, "authkey", "", "auth key to log in without a browser (or set TS_AUTHKEY)")
	fs.BoolVar(&opts.Ephemeral, "ephemeral", false, "remove the node from the tailnet on exit")
	fs.BoolVar(&opts.Verbose, "verbose", false, "log tsnet internals")
}

// commonFlags are the node flags shared by up, serve and login.
type commonFlags struct {
	Hostname  string
	StateDir  string
	AuthKey   string
	Ephemeral bool
	Verbose   bool
}

// nodeConfig turns the shared flags into a node.Config.
func (c commonFlags) nodeConfig(logf func(string, ...any)) node.Config {
	return node.Config{
		Hostname:  c.Hostname,
		StateDir:  c.StateDir,
		AuthKey:   c.AuthKey,
		Ephemeral: c.Ephemeral,
		Verbose:   c.Verbose,
		Logf:      logf,
	}
}
