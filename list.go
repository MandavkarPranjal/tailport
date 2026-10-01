package main

import (
	"fmt"

	"github.com/MandavkarPranjal/tailport/internal/netports"
	"github.com/MandavkarPranjal/tailport/internal/ui"
)

// listPorts shows what is listening on this machine, which is the same list the
// port menu offers.
func listPorts(env *ui.Env, args []string) error {
	fs := newFlagSet("list", env, usage)
	limit := fs.Int("limit", 0, "show at most this many ports")
	wide := fs.Bool("wide", false, "include the bound address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usageError("unexpected argument %q", rest[0])
	}
	return printPorts(env, *limit, *wide)
}

func printPorts(env *ui.Env, limit int, wide bool) error {
	listeners, err := netports.List()
	if err != nil {
		return err
	}
	if len(listeners) == 0 {
		env.Title("No local ports are listening")
		env.Detail("Start a dev server, then run tailport again.")
		return nil
	}

	shown := listeners
	if limit > 0 && limit < len(shown) {
		shown = shown[:limit]
	}
	env.Title("Local ports that can be shared")
	for _, l := range shown {
		note := l.String()
		if wide {
			note = l.Addr + "  " + note
		}
		env.Line("  %s  %s", env.Bold(fmt.Sprintf("%5d", l.Port)), env.Dim(note))
	}
	if len(shown) < len(listeners) {
		env.Detail("and %d more; use -limit 0 to see all", len(listeners)-len(shown))
	}
	env.Line("")
	env.Detail("Share one with: tailport up -p PORT")
	return nil
}
