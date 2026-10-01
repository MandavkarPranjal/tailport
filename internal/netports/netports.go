// Package netports discovers the TCP ports this machine is listening on and
// which process owns each one.
//
// Discovery is Linux specific: it reads /proc/net/tcp{,6} for listening
// sockets and /proc/<pid>/fd to attribute sockets to processes. On other
// systems List returns no entries and the CLI falls back to asking for a port.
package netports

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Listener is a TCP socket in the LISTEN state.
type Listener struct {
	// Port is the local TCP port.
	Port int
	// Addr is the local address, e.g. "127.0.0.1" or "0.0.0.0".
	Addr string
	// Wildcard is true when the socket accepts on every interface.
	Wildcard bool
	// Loopback is true when the socket accepts on loopback addresses only.
	Loopback bool
	// Process is the executable name, when it could be resolved.
	Process string
	// PID owns the socket, or 0 when unknown.
	PID int
}

// String renders the listener for the selection menu.
func (l Listener) String() string {
	switch {
	case l.Process != "" && l.PID != 0:
		return fmt.Sprintf("%s (%d)", l.Process, l.PID)
	case l.Process != "":
		return l.Process
	case l.Wildcard:
		return "all interfaces"
	default:
		return l.Addr
	}
}

// List returns every listening TCP port, sorted by port then by PID.
func List() ([]Listener, error) {
	socks, err := readSockets()
	if err != nil {
		return nil, err
	}
	inodes := make(map[uint64]struct{}, len(socks))
	for _, sock := range socks {
		inodes[sock.inode] = struct{}{}
	}
	owners := lookupOwners(inodes)

	out := make([]Listener, 0, len(socks))
	for _, sock := range socks {
		owner := owners[sock.inode]
		out = append(out, Listener{
			Port:     sock.port,
			Addr:     sock.addr,
			Wildcard: sock.addr == "0.0.0.0" || sock.addr == "::",
			Loopback: isLoopback(sock.addr),
			Process:  owner.name,
			PID:      owner.pid,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].PID < out[j].PID
	})
	return dedupe(out), nil
}

// IsListening reports whether something is accepting on the local port.
func IsListening(port int) bool {
	listeners, err := List()
	if err != nil {
		return false
	}
	for _, l := range listeners {
		if l.Port == port {
			return true
		}
	}
	return false
}

type socket struct {
	port  int
	addr  string
	inode uint64
}

func readSockets() ([]socket, error) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		return nil, fmt.Errorf("port discovery needs /proc: %w", err)
	}
	names := []string{"/proc/net/tcp", "/proc/net/tcp6"}
	var out []socket
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		out = append(out, parseProcNet(string(data))...)
	}
	return out, nil
}

// parseProcNet extracts listening sockets from the contents of a
// /proc/net/tcp or /proc/net/tcp6 file. Only the LISTEN state (0A) is kept.
func parseProcNet(data string) []socket {
	var out []socket
	scanner := bufio.NewScanner(strings.NewReader(data))
	for lineNo := 0; scanner.Scan(); lineNo++ {
		if lineNo == 0 {
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		addr, port, ok := decodeAddr(fields[1])
		if !ok {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, socket{port: port, addr: addr, inode: inode})
	}
	return out
}

// decodeAddr splits a /proc local_address field ("0100007F:1F90") into an IP
// and a port. The kernel stores each 32-bit group in host byte order, so the
// bytes of every group are reversed.
func decodeAddr(field string) (string, int, bool) {
	host, portHex, ok := strings.Cut(field, ":")
	if !ok {
		return "", 0, false
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return "", 0, false
	}
	raw, err := hex.DecodeString(host)
	if err != nil {
		return "", 0, false
	}
	switch len(raw) {
	case net.IPv4len:
		return net.IPv4(raw[3], raw[2], raw[1], raw[0]).String(), int(port), true
	case net.IPv6len:
		ip := make(net.IP, net.IPv6len)
		for group := range 4 {
			for b := range 4 {
				ip[group*4+b] = raw[group*4+3-b]
			}
		}
		return ip.String(), int(port), true
	default:
		return "", 0, false
	}
}

type owner struct {
	name string
	pid  int
}

// lookupOwners maps socket inodes to the process holding them open. A process
// may own several sockets, so /proc is walked once and each fd readlink is
// attributed to the first process that claims the inode.
func lookupOwners(inodes map[uint64]struct{}) map[uint64]owner {
	owners := make(map[uint64]owner, len(inodes))
	pids, err := os.ReadDir("/proc")
	if err != nil {
		return owners
	}
	for _, entry := range pids {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		var name string
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			inode, ok := socketInode(link)
			if !ok {
				continue
			}
			if _, wanted := inodes[inode]; !wanted {
				continue
			}
			if _, done := owners[inode]; done {
				continue
			}
			if name == "" {
				name = processName(pid)
			}
			owners[inode] = owner{name: name, pid: pid}
		}
	}
	return owners
}

func socketInode(link string) (uint64, bool) {
	rest, ok := strings.CutPrefix(link, "socket:[")
	if !ok {
		return 0, false
	}
	inode, ok := strings.CutSuffix(rest, "]")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(inode, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func processName(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func isLoopback(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

func dedupe(in []Listener) []Listener {
	out := in[:0]
	var last Listener
	for i, l := range in {
		if i > 0 && l.Port == last.Port && l.Addr == last.Addr {
			continue
		}
		out = append(out, l)
		last = l
	}
	return out
}
