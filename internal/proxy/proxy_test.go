package proxy

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestTargetRendering(t *testing.T) {
	target := Target{Host: "127.0.0.1", Port: 3000}
	if got, want := target.String(), "127.0.0.1:3000"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
	if got, want := target.URL(), "http://127.0.0.1:3000"; got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
	if got, want := Loopback(8080).String(), "127.0.0.1:8080"; got != want {
		t.Errorf("Loopback(8080) = %q, want %q", got, want)
	}
}

func TestHandlerProxiesToUpstream(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
		_, _ = io.WriteString(w, "hello from upstream")
	}))
	defer upstream.Close()

	host, port := splitAddr(t, upstream.URL)
	handler := Handler(Target{Host: host, Port: port}, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/deep/path", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "hello from upstream" {
		t.Errorf("body = %q, want the upstream body", got)
	}
	if seen != "/deep/path" {
		t.Errorf("upstream saw path %q, want /deep/path", seen)
	}
}

func TestHandlerKeepsTheOriginalHost(t *testing.T) {
	var gotHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
	}))
	defer upstream.Close()

	host, port := splitAddr(t, upstream.URL)
	req := httptest.NewRequest(http.MethodGet, "http://tailport/", nil)
	rec := httptest.NewRecorder()
	Handler(Target{Host: host, Port: port}, nil).ServeHTTP(rec, req)

	if gotHost != "tailport" {
		t.Errorf("upstream Host = %q, want tailport", gotHost)
	}
}

func TestHandlerReportsAnUnreachableUpstream(t *testing.T) {
	var logged strings.Builder
	logger := log.New(&logged, "", 0)

	// Port 1 on loopback has nothing listening, so the proxy must fail fast.
	rec := httptest.NewRecorder()
	Handler(Loopback(1), logger).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tailport cannot reach 127.0.0.1:1") {
		t.Errorf("body = %q, want it to name the unreachable target", rec.Body.String())
	}
	if !strings.Contains(logged.String(), "proxy GET /") {
		t.Errorf("log = %q, want the failed request logged", logged.String())
	}
}

func TestHandlerSurvivesANilLogger(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(Loopback(1), nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 without a logger", rec.Code)
	}
}

func TestMountStripsThePrefix(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
	}))
	defer upstream.Close()

	host, port := splitAddr(t, upstream.URL)
	handler := Mount(Target{Host: host, Port: port}, "/demo", nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/page", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if seen != "/page" {
		t.Errorf("upstream saw path %q, want /page", seen)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("outside-prefix status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/demo/") {
		t.Errorf("404 body = %q, want it to point at /demo/", rec.Body.String())
	}
}

func TestMountRoutesTheBarePrefixToTheServiceRoot(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
	}))
	defer upstream.Close()

	host, port := splitAddr(t, upstream.URL)
	handler := Mount(Target{Host: host, Port: port}, "/demo", nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("bare prefix status = %d, want 200", rec.Code)
	}
	if seen != "/" {
		t.Errorf("upstream saw path %q for the bare prefix, want /", seen)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/demo/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("prefix with slash status = %d, want 200", rec.Code)
	}
	if seen != "/" {
		t.Errorf("upstream saw path %q for /demo/, want /", seen)
	}
}

func TestMountTreatsAnEmptyPrefixAsEverything(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path
	}))
	defer upstream.Close()

	host, port := splitAddr(t, upstream.URL)
	for _, prefix := range []string{"", "/"} {
		rec := httptest.NewRecorder()
		Mount(Target{Host: host, Port: port}, prefix, nil).
			ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("prefix %q: status = %d, want 200", prefix, rec.Code)
		}
		if seen != "/anything" {
			t.Errorf("prefix %q: upstream saw %q, want /anything", prefix, seen)
		}
	}
}

func TestServerSetsTimeouts(t *testing.T) {
	srv := Server(http.NotFoundHandler())
	if srv.ReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout is not set")
	}
	if srv.IdleTimeout == 0 {
		t.Error("IdleTimeout is not set")
	}
	if srv.Handler == nil {
		t.Error("Handler is not set")
	}
}

// splitAddr pulls host and port out of an httptest server URL.
func splitAddr(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split %q: %v", u.Host, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	return host, port
}
