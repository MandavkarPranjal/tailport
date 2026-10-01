# tailport

Share a local port with your tailnet and, by default, the whole internet.

tailport runs its own Tailscale node named `tailport`, so every install gets the
same friendly addresses on its own tailnet:

```
http://tailport                     inside your tailnet
https://tailport.<your-tailnet>.ts.net   on the public web
```

Run it with no arguments and it lists the ports listening on the machine and
asks which one to share.

## Install

```sh
go install github.com/MandavkarPranjal/tailport@latest
```

You need the Tailscale CLI-less side only, not the Tailscale app: tailport
embeds its own node through [tsnet](https://pkg.go.dev/tailscale.com/tsnet), so
it needs no root and no `tailscaled`.

On the first run tailport prints a login link. Opening it authorises the
`tailport` node on your tailnet. To skip the browser, create an auth key in the
Tailscale admin console and pass it:

```sh
tailport login --authkey tskey-auth-xxxx
# or export TS_AUTHKEY=tskey-auth-xxxx for one run
```

Funnel also has to be enabled for your tailnet, and HTTPS certificates for it,
both under <https://login.tailscale.com/admin/dns>. Without them `tailport`
says so and points you at the right page.

## Use

```sh
tailport                          # menu of listening ports, then share the one you pick
tailport up -p 3000               # share port 3000 on the public web
tailport up -p 3000 --private     # share it inside your tailnet only
tailport up -p 3000 --all         # publish on 443, 8443 and 10000
tailport up -p 3000 --path /demo  # publish under a URL prefix
tailport up -p 3000 -d            # keep running in the background
tailport status                   # what is shared, and since when
tailport stop                     # stop everything
tailport stop 3000                # stop one port
tailport list                     # ports on this machine that can be shared
```

In the menu, `j` and `k` or the arrow keys move the highlight, `enter` shares
what is highlighted, `g` and `G` jump to the top and bottom, `q` quits, and
typing digits picks a row or gives a port directly. `/` filters the rows as you
type, matching the port, the process name or the address; while filtering, the
arrow keys move between the matches and `esc` clears the filter.

Sharing runs in the foreground and stops with Ctrl-C. With `-d` tailport
re-executes itself in the background, records itself so `status` and `stop` can
find it, and writes to a log under its state directory.

Funnel can only publish ports 443, 8443 and 10000, since those are where it
terminates TLS. `--public-port` picks a different one of those; `--all` takes all
three at once.

## Flags

| Flag | Meaning |
| --- | --- |
| `-p`, `--port PORT` | local port to share, skips the menu |
| `--all` | publish on every port Funnel supports (443, 8443, 10000) |
| `--public-port PORT` | publish on PORT instead of 443 |
| `--path PATH` | publish under a URL prefix, e.g. `/demo` |
| `--private` | share inside the tailnet only |
| `-d`, `--daemon` | run in the background |
| `-y`, `--yes` | never prompt, requires `--port` |
| `--hostname NAME` | node name in your tailnet, default `tailport` |
| `--state-dir DIR` | where to keep node credentials |
| `--authkey KEY` | auth key to log in without a browser |
| `--ephemeral` | remove the node from your tailnet when tailport exits |
| `--verbose` | log tsnet internals |

## Several at once

Each run is a separate process with its own record, so different ports can be
shared at the same time. Two runs cannot use the same node name, since they
would fight over the same Tailscale node; give one of them a different
`--hostname`, or `--ephemeral` so it leaves the tailnet on exit.

```sh
tailport up -p 3000 --hostname frontend &
tailport up -p 8080 --hostname api
```

## Security

Anything published with Funnel is reachable by anyone on the internet with the
link, so put a login page in front of anything that matters, or use `--private`.
Funnel is meant for demos and previews; it serves plain HTTP to the world and
the address is a secret, not a control.

## How it works

`internal/node` starts the embedded tsnet node and opens the listeners;
`internal/proxy` forwards each request to `127.0.0.1:<port>`; `internal/state`
keeps the run records `status` and `stop` read; `internal/netports` finds the
ports listening on the machine; `internal/daemon` handles backgrounding by
re-executing the same binary.

## Development

```sh
go test ./...
go vet ./...
```