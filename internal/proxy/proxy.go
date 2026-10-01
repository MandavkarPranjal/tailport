// Package proxy forwards requests from the tailport node to the local port the
// user picked.
package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Target is the local address tailport forwards to, e.g. 127.0.0.1:3000.
type Target struct {
	// Host is the local host to dial, e.g. 127.0.0.1.
	Host string
	// Port is the local TCP port to dial.
	Port int
}

// String renders the target as host:port.
func (t Target) String() string {
	return t.Host + ":" + strconv.Itoa(t.Port)
}

// URL is the http:// URL of the target.
func (t Target) URL() string {
	return "http://" + t.String()
}

// Loopback returns a Target for a port on the local machine.
func Loopback(port int) Target {
	return Target{Host: "127.0.0.1", Port: port}
}

// Handler returns an http.Handler that proxies every request to target. It
// keeps the original Host header so local dev servers that check it still work,
// and it logs upstream failures instead of dropping them silently.
func Handler(target Target, logger *log.Logger) http.Handler {
	u := &url.URL{Scheme: "http", Host: target.String()}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = r.In.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if logger != nil {
				logger.Printf("proxy %s %s: %v", r.Method, r.URL.Path, err)
			}
			http.Error(w, "tailport cannot reach "+target.String(), http.StatusBadGateway)
		},
	}
	return proxy
}

// Mount returns target behind a path prefix, so a service that does not expect
// a prefix can be published at one. A request outside the prefix gets a short
// page pointing at the right URL. An empty prefix proxies everything.
func Mount(target Target, prefix string, logger *log.Logger) http.Handler {
	inner := Handler(target, logger)
	clean := "/" + strings.Trim(prefix, "/")
	if clean == "/" {
		return inner
	}

	// Register both /demo and /demo/ so the service answers with and without the
	// trailing slash, and turn the bare prefix into a root path the service can
	// still route.
	mounted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == clean {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		http.StripPrefix(clean, inner).ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	mux.Handle(clean, mounted)
	mux.Handle(clean+"/", mounted)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8>" +
			"<title>tailport</title><p>Served at <a href=\"" + clean + "/\">" + clean + "/</a>."))
	})
	return mux
}

// Server is an http.Server wired to a handler, with the timeouts a tunnel needs.
func Server(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
