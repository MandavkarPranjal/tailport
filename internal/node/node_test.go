package node

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"tailscale.com/ipn/ipnstate"
)

func TestIsPublicPort(t *testing.T) {
	for _, port := range []int{443, 8443, 10000} {
		if !IsPublicPort(port) {
			t.Errorf("IsPublicPort(%d) = false, want true", port)
		}
	}
	for _, port := range []int{0, 80, 3000, 65535} {
		if IsPublicPort(port) {
			t.Errorf("IsPublicPort(%d) = true, want false", port)
		}
	}
}

func TestIsValidPort(t *testing.T) {
	tests := map[int]bool{
		0: false, -1: false, 1: true, 3000: true, 65535: true, 65536: false,
	}
	for port, want := range tests {
		if got := IsValidPort(port); got != want {
			t.Errorf("IsValidPort(%d) = %v, want %v", port, got, want)
		}
	}
}

func TestPortsList(t *testing.T) {
	if got, want := PortsList(), "443, 8443, 10000"; got != want {
		t.Errorf("PortsList() = %q, want %q", got, want)
	}
}

func TestDefaultPublicPortIsPublishable(t *testing.T) {
	if !IsPublicPort(DefaultPublicPort) {
		t.Fatalf("DefaultPublicPort %d is not in PublicPorts", DefaultPublicPort)
	}
}

func TestStateDirPrefersEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TAILPORT_STATE_DIR", dir)
	if got := StateDir(); got != dir {
		t.Errorf("StateDir() = %q, want %q", got, dir)
	}
}

func TestStateDirDefaultsUnderUserConfig(t *testing.T) {
	t.Setenv("TAILPORT_STATE_DIR", "")
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	got := StateDir()
	if want := filepath.Join(configHome, Name); got != want {
		t.Errorf("StateDir() = %q, want %q", got, want)
	}
}

func TestHostnamePrefersEnv(t *testing.T) {
	t.Setenv("TAILPORT_HOSTNAME", "other")
	if got := Hostname(); got != "other" {
		t.Errorf("Hostname() = %q, want %q", got, "other")
	}
	t.Setenv("TAILPORT_HOSTNAME", "")
	if got := Hostname(); got != Name {
		t.Errorf("Hostname() = %q, want %q", got, Name)
	}
}

func TestDNSNameFrom(t *testing.T) {
	tests := []struct {
		name string
		st   *ipnstate.Status
		want string
	}{
		{name: "nil status"},
		{name: "prefers self dns name", st: &ipnstate.Status{
			Self:        &ipnstate.PeerStatus{DNSName: "tailport.tailc1e3a1.ts.net."},
			CertDomains: []string{"other.tailc1e3a1.ts.net"},
		}, want: "tailport.tailc1e3a1.ts.net"},
		{name: "falls back to cert domain", st: &ipnstate.Status{
			CertDomains: []string{"tailport.example.ts.net."},
		}, want: "tailport.example.ts.net"},
		{name: "no names at all", st: &ipnstate.Status{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dnsNameFrom(tt.st); got != tt.want {
				t.Errorf("dnsNameFrom() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNodeURLs(t *testing.T) {
	n := &tsnetNode{hostname: Name, dnsName: "tailport.tailc1e3a1.ts.net"}
	if got, want := n.TailnetURL(), "http://tailport"; got != want {
		t.Errorf("TailnetURL() = %q, want %q", got, want)
	}
	if got, want := n.PublicURL(443), "https://tailport.tailc1e3a1.ts.net"; got != want {
		t.Errorf("PublicURL(443) = %q, want %q", got, want)
	}
	if got, want := n.PublicURL(8443), "https://tailport.tailc1e3a1.ts.net:8443"; got != want {
		t.Errorf("PublicURL(8443) = %q, want %q", got, want)
	}

	noDNS := &tsnetNode{hostname: Name}
	if got, want := noDNS.PublicURL(8443), "https://"; got != want {
		t.Errorf("PublicURL without DNS = %q, want %q", got, want)
	}
}

func TestClosedNodeRefusesListeners(t *testing.T) {
	n := &tsnetNode{closed: true}
	if _, err := n.ListenTailnet(80); !errors.Is(err, ErrClosed) {
		t.Errorf("ListenTailnet on closed node = %v, want ErrClosed", err)
	}
	if _, err := n.ListenFunnel(443); !errors.Is(err, ErrClosed) {
		t.Errorf("ListenFunnel on closed node = %v, want ErrClosed", err)
	}
}

func TestCloseIsSafeOnNilAndTwice(t *testing.T) {
	var n *tsnetNode
	if err := n.Close(); err != nil {
		t.Errorf("Close(nil) = %v, want nil", err)
	}
	if err := (&tsnetNode{}).Close(); err != nil {
		t.Errorf("Close on zero node = %v, want nil", err)
	}
}

func TestListenTailnetRejectsBadPort(t *testing.T) {
	n := &tsnetNode{}
	_, err := n.ListenTailnet(0)
	if err == nil || !strings.Contains(err.Error(), "not a valid port") {
		t.Fatalf("ListenTailnet(0) = %v, want an invalid port error", err)
	}
}

func TestListenFunnelRejectsUnpublishablePort(t *testing.T) {
	n := &tsnetNode{}
	_, err := n.ListenFunnel(3000)
	if err == nil {
		t.Fatal("ListenFunnel(3000) = nil error, want a rejection")
	}
	for _, want := range []string{"3000", "443", "8443", "10000"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestHintExplainsSetupProblems(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil},
		{name: "needs login", err: errors.New("NeedsLogin"), want: "TS_AUTHKEY"},
		{name: "deadline", err: errors.New("context deadline exceeded"), want: "TS_AUTHKEY"},
		{name: "https off", err: errors.New("Funnel not available; HTTPS must be enabled"), want: "admin/dns"},
		{name: "magicdns off", err: errors.New("MagicDNS name is not available"), want: "MagicDNS"},
		{name: "funnel policy", err: errors.New("funnel access denied by tailnet policy"), want: "policy"},
		{name: "unrelated", err: errors.New("dial tcp 1.2.3.4:80: refused"), want: "dial tcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Hint(tt.err)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("Hint(nil) = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("Hint(%v) = nil, want advice", tt.err)
			}
			if !strings.Contains(got.Error(), tt.want) {
				t.Errorf("Hint(%v) = %q, want it to mention %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestHintWrapsTheOriginalError(t *testing.T) {
	base := errors.New("boom")
	if err := Hint(base); !errors.Is(err, base) {
		t.Errorf("Hint(%v) = %v, want it to wrap the original", base, err)
	}
}

func TestWatchAuthURLIsANoOpWithoutCallback(t *testing.T) {
	watchAuthURL(t.Context(), nil, nil)()
}
