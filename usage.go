package main

import (
	"fmt"
	"io"

	"github.com/MandavkarPranjal/tailport/internal/node"
)

// usage is the help text for "tailport help" and for any command-line mistake.
func usage(w io.Writer) {
	ports := node.PortsList()
	fmt.Fprintf(w, `tailport shares a local port with your tailnet and, by default, the public web.

It runs its own Tailscale node named %q, so every user of this tool gets the same
friendly addresses on their own tailnet:

  http://%[1]s                        inside your tailnet
  https://%[1]s.<tailnet>.ts.net     on the public web

Usage:
  tailport                        pick a port from a menu and share it
  tailport up [flags]             share a port, given or chosen
  tailport status                 show what tailport is sharing right now
  tailport watch [port]           watch the requests a share is serving, live
  tailport stop [port]            stop sharing, all of it or just one port
  tailport list                   list local listening ports
  tailport login                  log the %[1]s node in ahead of time
  tailport version                print the version
  tailport help                   print this text

Run "tailport up -h" or "tailport watch -h" for the flags of a command.

Sharing flags:
  -p, --port PORT        local port to share (skips the menu)
      --all              publish on every port Funnel supports (%[2]s)
      --public-port PORT publish on PORT instead of %[3]d (%[2]s)
      --path PATH        publish under this URL prefix, e.g. /demo
      --private          share inside the tailnet only, never on the public web
  -d, --daemon           run in the background and keep serving after tailport exits
  -y, --yes              never prompt; requires --port

Watch flags:
  -n, --lines N          past requests to show before the live feed (default %[4]d;
                         0 shows only what arrives from now on)

Node flags:
      --hostname NAME    node name in your tailnet (default %[1]q)
      --state-dir DIR    where to keep node credentials
      --authkey KEY      auth key to log in without a browser (or set TS_AUTHKEY)
      --ephemeral        remove the node from your tailnet when tailport exits
      --verbose          log tsnet internals

Examples:
  tailport                         menu of your listening ports, then share it
  tailport up -p 3000              share port 3000 on the public web
  tailport up -p 3000 --private    share port 3000 inside the tailnet only
  tailport up -p 3000 -d           share port 3000 in the background
  tailport status                  what is shared, and since when
  tailport watch                   live request log for a share
  tailport watch 3000              live request log for port 3000
  tailport watch -n 0              live request log, past requests left out
  tailport stop                    stop sharing everything
`,
		node.Name, ports, node.DefaultPublicPort, historyLines)
}
