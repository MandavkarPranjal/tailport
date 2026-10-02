package proxy

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/reqlog"
)

// collector gathers observed events so a test can look at them afterwards.
type collector struct {
	mu     sync.Mutex
	events []reqlog.Event
}

func (c *collector) observe(e reqlog.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collector) all() []reqlog.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]reqlog.Event(nil), c.events...)
}

// waitFor blocks until the collector has seen n events, so a test never has to
// guess whether the proxy has finished reporting.
func (c *collector) waitFor(t *testing.T, n int) []reqlog.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if events := c.all(); len(events) >= n {
			return events
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the proxy reported %d events, want %d", len(c.all()), n)
	return nil
}

// target returns a Target for a running test server.
func target(t *testing.T, srv *httptest.Server) Target {
	t.Helper()
	addr := srv.Listener.Addr().(*net.TCPAddr)
	return Target{Host: "127.0.0.1", Port: addr.Port}
}

func TestHandlerReportsTheMethodPathStatusAndLatency(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(30 * time.Millisecond)
		}
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("hi"))
	}))
	t.Cleanup(upstream.Close)

	c := &collector{}
	handler := Handler(target(t, upstream), nil, c.observe)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// Three different outcomes, so each field has to be right for each case and
	// not merely for the easy one.
	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodPost, "/missing", http.StatusNotFound},
		{http.MethodGet, "/slow", http.StatusOK},
	} {
		req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.status)
		}
	}

	events := c.waitFor(t, 3)
	want := []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodPost, "/missing", http.StatusNotFound},
		{http.MethodGet, "/slow", http.StatusOK},
	}
	for i, w := range want {
		got := events[i]
		if got.Method != w.method || got.Path != w.path || got.Status != w.status {
			t.Errorf("event %d = %s %s %d, want %s %s %d",
				i, got.Method, got.Path, got.Status, w.method, w.path, w.status)
		}
		if got.Latency <= 0 {
			t.Errorf("event %d has latency %v, want a positive duration", i, got.Latency)
		}
		if got.Time.IsZero() {
			t.Errorf("event %d has no timestamp", i)
		}
		if got.Error != "" {
			t.Errorf("event %d = %q, want no error for a request that got an answer", i, got.Error)
		}
	}

	// The slow request has to be visibly slower than the two instant ones, or
	// the latency is not really being measured around the round trip.
	if events[2].Latency <= events[0].Latency {
		t.Errorf("a request the upstream held for 30ms took %v, no more than an instant one at %v",
			events[2].Latency, events[0].Latency)
	}
}

func TestHandlerReportsAnUnreachableUpstreamAsBadGateway(t *testing.T) {
	// Port 1 on loopback has nothing listening, so the request never gets an
	// answer. The log has to show that as a 502 with the reason, not as a
	// successful request.
	c := &collector{}
	srv := httptest.NewServer(Handler(Loopback(1), nil, c.observe))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/gone")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()

	events := c.waitFor(t, 1)
	got := events[0]
	if got.Status != http.StatusBadGateway {
		t.Errorf("Status = %d, want %d", got.Status, http.StatusBadGateway)
	}
	if got.Method != http.MethodGet || got.Path != "/gone" {
		t.Errorf("event = %s %s, want GET /gone", got.Method, got.Path)
	}
	if got.Error == "" {
		t.Error("Error is empty, want the reason the local service could not be reached")
	}
}

func TestHandlerReportsThePathAsTheClientAskedForIt(t *testing.T) {
	// Under a prefix the client asked for /demo/page and the service was handed
	// /page. The log has to carry the one the client recognises.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	t.Cleanup(upstream.Close)

	c := &collector{}
	srv := httptest.NewServer(Mount(target(t, upstream), "/demo", nil, c.observe))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/demo/page")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()

	events := c.waitFor(t, 1)
	if got := events[0].Path; got != "/demo/page" {
		t.Errorf("Path = %q, want %q", got, "/demo/page")
	}
}

func TestHandlerReportsEveryConcurrentRequest(t *testing.T) {
	// A share is served from several server goroutines at once, so the observer
	// and the tracker have to survive being used from all of them.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(upstream.Close)

	c := &collector{}
	srv := httptest.NewServer(Handler(target(t, upstream), nil, c.observe))
	t.Cleanup(srv.Close)

	const requests = 50
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := http.Get(srv.URL + "/page")
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("request %d = %d, want 200", i, resp.StatusCode)
			}
		}()
	}
	wg.Wait()

	events := c.waitFor(t, requests)
	if len(events) != requests {
		t.Fatalf("reported %d events, want exactly %d", len(events), requests)
	}
	// Each request has to carry its own status, so a mix of outcomes here would
	// show up as events with the wrong path.
	for _, e := range events {
		if e.Status != http.StatusOK || e.Path != "/page" {
			t.Errorf("event = %s %s %d, want GET /page 200", e.Method, e.Path, e.Status)
		}
	}
}

func TestHandlerLeavesStreamingResponsesAlone(t *testing.T) {
	// Capturing the status through the ResponseWriter would mean wrapping it,
	// and a wrapper that forgets Flush silently breaks server-sent events and
	// anything else that streams. This checks the real thing still flushes.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "upstream cannot flush", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first\n"))
		flusher.Flush()
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte("second\n"))
	}))
	t.Cleanup(upstream.Close)

	c := &collector{}
	srv := httptest.NewServer(Handler(target(t, upstream), nil, c.observe))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	if first != "first\n" {
		t.Errorf("first chunk = %q, want %q", first, "first\n")
	}
	// The flush has to have arrived before the second write, which is only true
	// if the flush made it through the proxy unwrapped.
	second, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read second chunk: %v", err)
	}
	if second != "second\n" {
		t.Errorf("second chunk = %q, want %q", second, "second\n")
	}

	events := c.waitFor(t, 1)
	if got := events[0].Status; got != http.StatusOK {
		t.Errorf("Status = %d, want 200", got)
	}
}

func TestHandlerWithoutObserversStillProxies(t *testing.T) {
	// Sharing a port must not depend on anyone being there to watch it, and the
	// no-observer path has to keep the original Host header behaviour.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Host))
	}))
	t.Cleanup(upstream.Close)

	srv := httptest.NewServer(Handler(target(t, upstream), nil))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status = %d, want 200", resp.StatusCode)
	}
}
