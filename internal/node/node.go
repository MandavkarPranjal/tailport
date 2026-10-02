// Package node runs tailport's own embedded Tailscale node. Every user of
// tailport gets a node named "tailport" of their own, so the tailnet URL is
// http://tailport and the public Funnel URL is
// https://tailport.<tailnet>.ts.net.
package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// Name is the node name tailport presents to the tailnet. It gives every user
// the same friendly URLs inside their own tailnet: http://tailport.
const Name = "tailport"

// PublicPorts are the only ports Tailscale Funnel can publish.
var PublicPorts = []int{443, 8443, 10000}

// DefaultPublicPort is the Funnel port used when none is asked for.
const DefaultPublicPort = 443

// ErrClosed is returned when a node is used after Close.
var ErrClosed = errors.New("node is closed")

// IsPublicPort reports whether Funnel can publish port.
func IsPublicPort(port int) bool {
	for _, p := range PublicPorts {
		if p == port {
			return true
		}
	}
	return false
}

// IsValidPort reports whether port is a usable local TCP port.
func IsValidPort(port int) bool {
	return port > 0 && port < 65536
}

// PortsList renders PublicPorts for help text and error messages.
func PortsList() string {
	parts := make([]string, len(PublicPorts))
	for i, p := range PublicPorts {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ", ")
}

// StateDir is where tailport keeps its node credentials, so a user logs in once
// per machine. TAILPORT_STATE_DIR overrides it.
func StateDir() string {
	if dir := os.Getenv("TAILPORT_STATE_DIR"); dir != "" {
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "." + string(filepath.Separator) + Name
	}
	return filepath.Join(base, Name)
}

// Hostname is the node's name in the tailnet. TAILPORT_HOSTNAME overrides it so
// one machine can run several tailports without colliding.
func Hostname() string {
	if name := os.Getenv("TAILPORT_HOSTNAME"); name != "" {
		return name
	}
	return Name
}

// Config configures a Node.
type Config struct {
	// Hostname is the node name. Empty means Hostname().
	Hostname string
	// StateDir holds the node credentials. Empty means StateDir().
	StateDir string
	// AuthKey logs the node in without a browser. Empty falls back to
	// TS_AUTHKEY, which tsnet reads itself.
	AuthKey string
	// Ephemeral removes the node from the tailnet when tailport exits.
	Ephemeral bool
	// Verbose logs tsnet's internal debug output.
	Verbose bool
	// Logf receives login URLs and other messages meant for the user.
	Logf func(format string, args ...any)
}

// Node is a running tailport node: somewhere a shared port is published, and the
// addresses it is published at. Commands talk to this interface rather than to
// tsnet directly, so the sharing path can be exercised without a tailnet.
type Node interface {
	// DNSName is the node's fully qualified name in the tailnet, for example
	// tailport.tailc1e3a1.ts.net. It is empty when the tailnet has no MagicDNS.
	DNSName() string
	// Hostname is the node's short name, Name unless overridden.
	Hostname() string
	// IPs are the node's tailnet addresses.
	IPs() []netip.Addr
	// TailnetURL is the URL that works from inside the tailnet.
	TailnetURL() string
	// PublicURL is the public URL for a published port.
	PublicURL(publicPort int) string
	// ListenTailnet opens a listener reachable only from inside the tailnet.
	ListenTailnet(port int) (net.Listener, error)
	// ListenFunnel opens a listener published to the public internet.
	ListenFunnel(publicPort int) (net.Listener, error)
	// Close shuts the node down. It is safe to call more than once.
	Close() error
}

// tsnetNode is the Node backed by a real embedded tsnet server.
type tsnetNode struct {
	srv      *tsnet.Server
	hostname string
	dnsName  string
	closed   bool
}

// Starter brings a node up. Commands take one of these instead of calling Start
// directly, so a test can substitute a Fake node for the embedded one.
type Starter func(ctx context.Context, cfg Config, onAuthURL func(string)) (Node, error)

// Start brings the embedded node up and waits until it is logged in and
// running. When interactive login is needed, onAuthURL is called with the URL
// to visit so the caller can show it to the user.
func Start(ctx context.Context, cfg Config, onAuthURL func(string)) (Node, error) {
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = Hostname()
	}
	dir := cfg.StateDir
	if dir == "" {
		dir = StateDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}

	srv := &tsnet.Server{
		Dir:       dir,
		Hostname:  hostname,
		AuthKey:   cfg.AuthKey,
		Ephemeral: cfg.Ephemeral,
		UserLogf:  cfg.Logf,
	}
	if cfg.Verbose {
		srv.Logf = func(format string, args ...any) {
			if cfg.Logf != nil {
				cfg.Logf("tsnet: "+format, args...)
			}
		}
	}

	type upResult struct {
		st  *ipnstate.Status
		err error
	}
	done := make(chan upResult, 1)
	go func() {
		st, err := srv.Up(ctx)
		done <- upResult{st: st, err: err}
	}()
	stopWatching := watchAuthURL(ctx, srv, onAuthURL)

	r := <-done
	stopWatching()

	if r.err != nil {
		srv.Close()
		return nil, fmt.Errorf("start tailport node: %w", r.err)
	}

	return &tsnetNode{srv: srv, hostname: hostname, dnsName: dnsNameFrom(r.st)}, nil
}

// dnsNameFrom prefers the node's own MagicDNS name and falls back to the first
// HTTPS cert domain, which is the same name on a tailnet without MagicDNS.
func dnsNameFrom(st *ipnstate.Status) string {
	if st == nil {
		return ""
	}
	if st.Self != nil && st.Self.DNSName != "" {
		return strings.TrimSuffix(st.Self.DNSName, ".")
	}
	if len(st.CertDomains) > 0 {
		return strings.TrimSuffix(st.CertDomains[0], ".")
	}
	return ""
}

// watchAuthURL polls the node while it waits for login so the caller can show
// the one-time auth URL. The returned func stops the poll.
func watchAuthURL(ctx context.Context, srv *tsnet.Server, onAuthURL func(string)) func() {
	if onAuthURL == nil {
		return func() {}
	}
	watchCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		seen := ""
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
			}
			lc, err := srv.LocalClient()
			if err != nil {
				continue
			}
			st, err := lc.Status(watchCtx)
			if err != nil {
				continue
			}
			if st.BackendState != "NeedsLogin" || st.AuthURL == "" || st.AuthURL == seen {
				continue
			}
			seen = st.AuthURL
			onAuthURL(st.AuthURL)
		}
	}()
	return cancel
}

// DNSName is the node's fully qualified name in the tailnet, for example
// tailport.tailc1e3a1.ts.net. It is empty when the tailnet has no MagicDNS.
func (n *tsnetNode) DNSName() string { return n.dnsName }

// Hostname is the node's short name, Name unless overridden.
func (n *tsnetNode) Hostname() string { return n.hostname }

// IPs are the node's tailnet addresses.
func (n *tsnetNode) IPs() []netip.Addr {
	ip4, ip6 := n.srv.TailscaleIPs()
	var out []netip.Addr
	for _, ip := range []netip.Addr{ip4, ip6} {
		if ip.IsValid() {
			out = append(out, ip)
		}
	}
	return out
}

// TailnetURL is the URL that works from inside the tailnet, http://tailport.
func (n *tsnetNode) TailnetURL() string {
	return "http://" + n.hostname
}

// PublicURL is the Funnel URL for a published port. Funnel terminates TLS, so
// every published port is https.
func (n *tsnetNode) PublicURL(publicPort int) string {
	if publicPort == DefaultPublicPort || n.dnsName == "" {
		return "https://" + n.dnsName
	}
	return "https://" + n.dnsName + ":" + strconv.Itoa(publicPort)
}

// ListenTailnet opens a listener reachable only from inside the tailnet.
func (n *tsnetNode) ListenTailnet(port int) (net.Listener, error) {
	if n.closed {
		return nil, ErrClosed
	}
	if !IsValidPort(port) {
		return nil, fmt.Errorf("%d is not a valid port", port)
	}
	ln, err := n.srv.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, fmt.Errorf("listen on tailnet port %d: %w", port, Hint(err))
	}
	return ln, nil
}

// ListenFunnel opens a listener published to the public internet on
// publicPort. Funnel only supports 443, 8443 and 10000.
func (n *tsnetNode) ListenFunnel(publicPort int) (net.Listener, error) {
	if n.closed {
		return nil, ErrClosed
	}
	if !IsPublicPort(publicPort) {
		return nil, fmt.Errorf("port %d cannot be published: use one of %s", publicPort, PortsList())
	}
	ln, err := n.srv.ListenFunnel("tcp", ":"+strconv.Itoa(publicPort))
	if err != nil {
		return nil, fmt.Errorf("publish port %d: %w", publicPort, Hint(err))
	}
	return ln, nil
}

// Close shuts the node down. It is safe to call more than once.
func (n *tsnetNode) Close() error {
	if n == nil || n.srv == nil || n.closed {
		return nil
	}
	n.closed = true
	return n.srv.Close()
}

// Hint turns the parts of tsnet's errors that are really setup problems into
// advice the user can act on.
func Hint(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "NeedsLogin"):
		return fmt.Errorf("%w\n  tailport could not finish logging in. Open the link it printed, or set TS_AUTHKEY to an auth key", err)
	case strings.Contains(msg, "HTTPS must be enabled"), strings.Contains(msg, "Funnel not available"):
		return fmt.Errorf("%w\n  enable HTTPS for your tailnet at https://login.tailscale.com/admin/dns", err)
	case strings.Contains(msg, "MagicDNS"):
		return fmt.Errorf("%w\n  enable MagicDNS for your tailnet at https://login.tailscale.com/admin/dns", err)
	case strings.Contains(msg, "funnel"), strings.Contains(msg, "Funnel"):
		return fmt.Errorf("%w\n  Funnel needs to be enabled for your tailnet, and your tailnet policy must allow it", err)
	default:
		return err
	}
}
