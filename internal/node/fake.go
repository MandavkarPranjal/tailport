package node

import (
	"fmt"
	"net"
	"net/netip"
	"sync"
)

// FakeConfig describes the Fake a test wants.
type FakeConfig struct {
	// Hostname is the short node name. Empty means Name.
	Hostname string
	// DNSName is the fully qualified node name. Empty derives one from
	// Hostname.
	DNSName string
}

// Fake is a Node that needs no tailnet, so tests can exercise the sharing path
// end to end without a login or a control plane. Its listeners are plain
// loopback TCP listeners, which makes them easy to drive with an http client.
type Fake struct {
	hostname string
	dnsName  string

	mu        sync.Mutex
	tailnet   []net.Listener
	published map[int]net.Listener
	closed    bool
}

// NewFake returns a Fake node, ready to hand out listeners.
func NewFake(cfg FakeConfig) *Fake {
	hostname := cfg.Hostname
	if hostname == "" {
		hostname = Name
	}
	dnsName := cfg.DNSName
	if dnsName == "" {
		dnsName = hostname + ".fake.ts.net"
	}
	return &Fake{
		hostname:  hostname,
		dnsName:   dnsName,
		published: map[int]net.Listener{},
	}
}

// Hostname is the node's short name.
func (f *Fake) Hostname() string { return f.hostname }

// DNSName is the node's fully qualified tailnet name.
func (f *Fake) DNSName() string { return f.dnsName }

// IPs are loopback addresses, so callers that report IPs get something usable.
func (f *Fake) IPs() []netip.Addr {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}
}

// TailnetURL is the loopback address of the first tailnet listener, so a test
// can fetch it. It is empty until ListenTailnet has been called.
func (f *Fake) TailnetURL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tailnet) == 0 {
		return ""
	}
	return "http://" + f.tailnet[0].Addr().String()
}

// PublicURL is the loopback address behind the given published port. Funnel
// terminates TLS on the real node, so this is the address underneath it.
func (f *Fake) PublicURL(publicPort int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ln := f.published[publicPort]
	if ln == nil {
		return ""
	}
	return "http://" + ln.Addr().String()
}

// ListenTailnet opens a loopback listener. The port is only checked for
// validity, since the OS picks the real one.
func (f *Fake) ListenTailnet(port int) (net.Listener, error) {
	if !IsValidPort(port) {
		return nil, fmt.Errorf("%d is not a valid port", port)
	}
	ln, err := f.listen()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = ln.Close()
		return nil, ErrClosed
	}
	f.tailnet = append(f.tailnet, ln)
	return ln, nil
}

// ListenFunnel opens a loopback listener for a published port and remembers it
// so PublicURL can report where it landed.
func (f *Fake) ListenFunnel(publicPort int) (net.Listener, error) {
	if !IsPublicPort(publicPort) {
		return nil, fmt.Errorf("port %d cannot be published: use one of %s", publicPort, PortsList())
	}
	ln, err := f.listen()
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		_ = ln.Close()
		return nil, ErrClosed
	}
	f.published[publicPort] = ln
	return ln, nil
}

// listen opens a loopback listener, or reports why it could not.
func (f *Fake) listen() (net.Listener, error) {
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	return ln, nil
}

// Close closes every listener Fake opened. It is safe to call more than once.
func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	var firstErr error
	for _, ln := range append(f.tailnet, listenerValues(f.published)...) {
		if err := ln.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// listenerValues returns the listeners in a published map.
func listenerValues(m map[int]net.Listener) []net.Listener {
	out := make([]net.Listener, 0, len(m))
	for _, ln := range m {
		out = append(out, ln)
	}
	return out
}
