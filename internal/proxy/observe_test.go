package proxy

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
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

// slowHold is how long the upstream below holds one request before answering.
// A sleep is a floor, never a ceiling, so the recorded latency for that request
// can be compared against it without the comparison being a coin toss.
const slowHold = 30 * time.Millisecond

func TestHandlerReportsTheMethodPathStatusAndLatency(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(slowHold)
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

	// The hold has to be in the recorded latency, which is what proves the
	// measurement wraps the whole round trip rather than something narrower. The
	// other two are only asked to be faster, never to be faster than each other:
	// comparing two instant requests against each other would fail whenever
	// either one happened to be descheduled, which says nothing about the code
	// under test.
	if events[2].Latency < slowHold {
		t.Errorf("a request the upstream held for %v took %v, want the hold included",
			slowHold, events[2].Latency)
	}
	for _, i := range []int{0, 1} {
		if events[i].Latency >= slowHold {
			t.Errorf("event %d took %v, want less than the %v the upstream held one request for",
				i, events[i].Latency, slowHold)
		}
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

// streamTimeout bounds the client's side of the streaming test. It is generous,
// because it only ever runs out when the thing under test is broken.
const streamTimeout = 5 * time.Second

func TestHandlerLeavesStreamingResponsesAlone(t *testing.T) {
	// Capturing the status through the ResponseWriter would mean wrapping it,
	// and a wrapper that forgets Flush silently breaks server-sent events and
	// anything else that streams. This checks the real thing still flushes.
	//
	// The upstream holds its second write back until this test has read the
	// first line. That is what makes the read mean something: if the proxy
	// swallowed the flush, the upstream would still be holding on, the first
	// line would never arrive on its own, and the read would run into the client
	// timeout instead of quietly succeeding with both lines once the handler
	// returned.
	released := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "upstream cannot flush", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first\n"))
		flusher.Flush()
		select {
		case <-released:
		case <-r.Context().Done():
			// The client gave up, which means it never saw the first line.
			return
		}
		_, _ = w.Write([]byte("second\n"))
	}))
	t.Cleanup(upstream.Close)

	c := &collector{}
	srv := httptest.NewServer(Handler(target(t, upstream), nil, c.observe))
	t.Cleanup(srv.Close)

	// The timeout covers the request and reading the body, so a lost flush
	// surfaces as a timeout rather than as a test that hangs until the binary's
	// own deadline. It fails at whichever read blocks first: with nothing pushed
	// to the client at all, that is the request itself.
	client := &http.Client{Timeout: streamTimeout}
	resp, err := client.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatalf("GET, with the upstream still holding its second write back: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	// A flush the proxy swallowed would leave the upstream holding its second
	// write back, waiting for a line that will never arrive on its own.
	first, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read the flushed chunk while the upstream was still holding its second write: %v", err)
	}
	if first != "first\n" {
		t.Errorf("first chunk = %q, want %q", first, "first\n")
	}
	close(released)

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

// A handler nobody is watching must not pay for the tracker at all. The proxy
// only learns a request's status through these callbacks, so their absence is
// what makes the unwatched path free.
func TestAnUnwatchedHandlerInstallsNoTrackingCallbacks(t *testing.T) {
	unwatched := newProxy(Loopback(3000), nil, false).(*httputil.ReverseProxy)
	if unwatched.ModifyResponse != nil {
		t.Error("ModifyResponse is set on an unwatched handler, want no per-request tracking")
	}
	watched := newProxy(Loopback(3000), nil, true).(*httputil.ReverseProxy)
	if watched.ModifyResponse == nil {
		t.Error("ModifyResponse is missing on a watched handler, want the status recorded")
	}
}

// Mount attaches its observers outside the proxy so it can report the path the
// client asked for, which means the proxy has to record the outcome from in
// here. If it stops doing that, every Mount request silently reports status 0.
func TestMountStillRecordsTheStatusWithItsObserversOutside(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(upstream.Close)

	c := &collector{}
	srv := httptest.NewServer(Mount(target(t, upstream), "/demo", nil, c.observe))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/demo/brew")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()

	events := c.waitFor(t, 1)
	if got := events[0].Status; got != http.StatusTeapot {
		t.Errorf("Status = %d, want %d recorded from inside the proxy", got, http.StatusTeapot)
	}
}

func TestMountStillRecordsAnUpstreamFailure(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(Mount(Loopback(1), "/demo", nil, c.observe))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/demo/anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()

	events := c.waitFor(t, 1)
	if got := events[0].Status; got != http.StatusBadGateway {
		t.Errorf("Status = %d, want 502", got)
	}
	if events[0].Error == "" {
		t.Error("Error is empty, want the upstream failure recorded")
	}
}

// An unwatched Mount must not track either, or every share nobody is watching
// would pay for a context lookup on every request.
func TestAnUnwatchedMountInstallsNoTrackingCallbacks(t *testing.T) {
	// Mount builds its inner proxy itself, so the flag it passes is what decides
	// this. Reaching for the handler's own observers would get it wrong, because
	// Mount's observers sit outside the proxy.
	handler := Mount(Loopback(1), "", nil)
	if _, ok := handler.(*httputil.ReverseProxy); !ok {
		t.Fatalf("an unwatched Mount returned %T, want the bare proxy with nothing wrapped on", handler)
	}
	if handler.(*httputil.ReverseProxy).ModifyResponse != nil {
		t.Error("ModifyResponse is set although nothing is watching")
	}
}

func TestMountReportsTheNotFoundPageItServesOutsideThePrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	c := &collector{}
	handler := Mount(target(t, upstream), "/demo", nil, c.observe)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/somewhere-else", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want the 404 page the prefix answers with", rec.Code)
	}

	// The client really did get a 404, so the watch has to say 404. Nothing
	// downstream of the mux writes a status for this response, so recording it is
	// the mux's own job.
	events := c.waitFor(t, 1)
	if events[0].Status != http.StatusNotFound {
		t.Errorf("Status = %d, want 404 for a request outside the prefix", events[0].Status)
	}
	if events[0].Path != "/somewhere-else" {
		t.Errorf("Path = %q, want the path the client asked for", events[0].Path)
	}
	if events[0].Error != "" {
		t.Errorf("Error = %q, want no error: the share answered, with a 404", events[0].Error)
	}
}
