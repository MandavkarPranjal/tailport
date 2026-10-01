package netports

import (
	"net"
	"strconv"
	"testing"
)

// A trimmed copy of /proc/net/tcp with one listener, one established socket
// and one non-listening socket.
const tcpFixture = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 26110 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 0100007F:C350 01 00000000:00000000 00:00000000 00000000     0        0 26111 1 0000000000000000 100 0 0 10 0
   2: 00000000:1388 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 26112 1 0000000000000000 100 0 0 10 0
`

const tcp6Fixture = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1F91 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 26200 1 0000000000000000 100 0 0 10 0
`

func TestParseProcNet(t *testing.T) {
	socks := parseProcNet(tcpFixture)
	if len(socks) != 2 {
		t.Fatalf("sockets = %d, want 2 (LISTEN only)", len(socks))
	}
	if socks[0].port != 3000 {
		t.Errorf("first port = %d, want 3000", socks[0].port)
	}
	if socks[0].addr != "0.0.0.0" {
		t.Errorf("first addr = %q, want 0.0.0.0", socks[0].addr)
	}
	if socks[0].inode != 26110 {
		t.Errorf("first inode = %d, want 26110", socks[0].inode)
	}
	if socks[1].port != 5000 {
		t.Errorf("second port = %d, want 5000", socks[1].port)
	}
}

func TestParseProcNetIPv6(t *testing.T) {
	socks := parseProcNet(tcp6Fixture)
	if len(socks) != 1 {
		t.Fatalf("sockets = %d, want 1", len(socks))
	}
	if socks[0].port != 8081 {
		t.Errorf("port = %d, want 8081", socks[0].port)
	}
	if socks[0].addr != "::1" {
		t.Errorf("addr = %q, want ::1 (little endian groups)", socks[0].addr)
	}
}

func TestParseProcNetEmptyAndHeaderOnly(t *testing.T) {
	if got := parseProcNet(""); got != nil {
		t.Errorf("parseProcNet(\"\") = %v, want nil", got)
	}
	headerOnly := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	if got := parseProcNet(headerOnly); got != nil {
		t.Errorf("header-only input = %v, want nil", got)
	}
}

func TestDecodeAddr(t *testing.T) {
	tests := []struct {
		field    string
		wantAddr string
		wantPort int
		wantOK   bool
	}{
		{"0100007F:1F90", "127.0.0.1", 8080, true},
		{"00000000:0BB8", "0.0.0.0", 3000, true},
		{"FFFFFFFF:FFFF", "255.255.255.255", 65535, true},
		{"00000000000000000000000001000000:1F91", "::1", 8081, true},
		{"nocolon", "", 0, false},
		{"ZZZZ:1F90", "", 0, false},
	}
	for _, tt := range tests {
		addr, port, ok := decodeAddr(tt.field)
		if ok != tt.wantOK {
			t.Errorf("decodeAddr(%q) ok = %v, want %v", tt.field, ok, tt.wantOK)
			continue
		}
		if ok && (addr != tt.wantAddr || port != tt.wantPort) {
			t.Errorf("decodeAddr(%q) = (%q, %d), want (%q, %d)", tt.field, addr, port, tt.wantAddr, tt.wantPort)
		}
	}
}

func TestSocketInode(t *testing.T) {
	inode, ok := socketInode("socket:[26110]")
	if !ok || inode != 26110 {
		t.Errorf("socketInode = (%d, %v), want (26110, true)", inode, ok)
	}
	for _, link := range []string{"/dev/null", "socket:[abc]", "socket:[26110", "pipe:[1]"} {
		if _, ok := socketInode(link); ok {
			t.Errorf("socketInode(%q) = true, want false", link)
		}
	}
}

func TestListenerString(t *testing.T) {
	tests := []struct {
		listener Listener
		want     string
	}{
		{Listener{Process: "node", PID: 4242}, "node (4242)"},
		{Listener{Process: "python3"}, "python3"},
		{Listener{Wildcard: true}, "all interfaces"},
		{Listener{Addr: "100.64.0.1"}, "100.64.0.1"},
	}
	for _, tt := range tests {
		if got := tt.listener.String(); got != tt.want {
			t.Errorf("Listener%+v.String() = %q, want %q", tt.listener, got, tt.want)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "127.0.0.53", "::1"} {
		if !isLoopback(addr) {
			t.Errorf("isLoopback(%q) = false, want true", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0", "192.168.1.5", "100.64.0.1", "not-an-ip", ""} {
		if isLoopback(addr) {
			t.Errorf("isLoopback(%q) = true, want false", addr)
		}
	}
}

func TestDedupe(t *testing.T) {
	in := []Listener{
		{Port: 80, Addr: "0.0.0.0", Process: "nginx", PID: 1},
		{Port: 80, Addr: "0.0.0.0", Process: "nginx", PID: 1},
		{Port: 80, Addr: "127.0.0.1", Process: "nginx", PID: 2},
		{Port: 443, Addr: "0.0.0.0"},
	}
	got := dedupe(in)
	if len(got) != 3 {
		t.Fatalf("dedupe kept %d entries, want 3", len(got))
	}
	if got[0].Port != 80 || got[1].Port != 80 || got[2].Port != 443 {
		t.Errorf("dedupe kept wrong entries: %+v", got)
	}
	if got[1].Addr != "127.0.0.1" {
		t.Errorf("second entry addr = %q, want 127.0.0.1", got[1].Addr)
	}
}

func TestListFindsOwnListener(t *testing.T) {
	if _, err := List(); err != nil {
		t.Skipf("no /proc on this system: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	if !IsListening(port) {
		t.Errorf("IsListening(%d) = false, want true", port)
	}
	listeners, err := List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	found := false
	for _, l := range listeners {
		if l.Port == port {
			found = true
			if !l.Loopback {
				t.Errorf("port %d should be flagged loopback (addr %q)", port, l.Addr)
			}
			break
		}
	}
	if !found {
		t.Errorf("port %d missing from List()", port)
	}
}

func TestIsListeningFalseForClosedPort(t *testing.T) {
	// Grab a port and release it so the number is almost certainly free.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if IsListening(port) {
		t.Errorf("IsListening(%d) = true after close", port)
	}
}

func TestPortsAreSortedAndValid(t *testing.T) {
	listeners, err := List()
	if err != nil {
		t.Skipf("no /proc on this system: %v", err)
	}
	prev := -1
	for _, l := range listeners {
		// Non-decreasing: one port can be bound on several addresses.
		if l.Port < prev {
			t.Errorf("ports out of order: %d after %d", l.Port, prev)
		}
		if !IsValidTCP(l.Port) {
			t.Errorf("implausible port %d", l.Port)
		}
		prev = l.Port
	}
}

func IsValidTCP(port int) bool {
	return port > 0 && port < 65536 && strconv.Itoa(port) != ""
}
