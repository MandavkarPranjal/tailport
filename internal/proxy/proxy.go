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
//
// Each observer is told about every request once the outcome is known. With no
// observers the handler does no extra work at all, which matters because this is
// the path every request through a share takes.
func Handler(target Target, logger *log.Logger, observers ...Observer) http.Handler {
	proxy := newProxy(target, logger, len(observers) > 0)
	if len(observers) == 0 {
		return proxy
	}
	return observe(proxy, observers...)
}

// newProxy builds the reverse proxy. track says whether some layer is watching
// the requests, which is not the same question as whether this handler has
// observers of its own: Mount observes outside the proxy so that it sees the
// path the client asked for, and it needs the outcome recorded from in here.
//
// When nothing is watching, the callbacks that would look for the tracker are not
// installed at all. Leaving them in costs a context lookup on every request that
// comes back from the upstream, which is every request, to find nothing.
func newProxy(target Target, logger *log.Logger, track bool) http.Handler {
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
			if track {
				if t := tracked(r); t != nil {
					t.status = http.StatusBadGateway
					t.err = err.Error()
				}
			}
			http.Error(w, "tailport cannot reach "+target.String(), http.StatusBadGateway)
		},
	}
	if track {
		proxy.ModifyResponse = func(resp *http.Response) error {
			// The upstream answered, so its status is the status the client gets.
			// ModifyResponse is not called for a request that failed to round
			// trip, so nothing here can overwrite a 502.
			if t := tracked(resp.Request); t != nil {
				t.status = resp.StatusCode
			}
			return nil
		}
	}
	return proxy
}

// Mount returns target behind a path prefix, so a service that does not expect
// a prefix can be published at one. A request outside the prefix gets a short
// page pointing at the right URL. An empty prefix proxies everything.
//
// Observers are reported at the outermost layer rather than inside the proxy,
// which keeps two things honest. The path is the one the client asked for, so a
// request to /demo/page is recorded as /demo/page even though the service was
// handed /page. And the 404 the prefix itself answers with is reported too,
// because the client really did get a 404; leaving it out would make the counts
// in a watch disagree with what the callers saw.
func Mount(target Target, prefix string, logger *log.Logger, observers ...Observer) http.Handler {
	// The observers are attached outside the proxy, so the proxy still has to
	// record the outcome. Asking for tracking here rather than passing the
	// observers down is what keeps that true.
	inner := newProxy(target, logger, len(observers) > 0)
	clean := "/" + strings.Trim(prefix, "/")
	if clean == "/" {
		if len(observers) == 0 {
			return inner
		}
		return observe(inner, observers...)
	}

	// Register both /demo and /demo/ so the service answers with and without the
	// trailing slash, and turn the bare prefix into a root path the service can
	// still route. Stripping has to happen before the rewrite, since StripPrefix
	// answers 404 once the prefix is no longer on the path.
	mounted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == clean {
			r = r.Clone(r.Context())
			r.URL.Path = clean + "/"
		}
		http.StripPrefix(clean, inner).ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	mux.Handle(clean, mounted)
	mux.Handle(clean+"/", mounted)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// This 404 is the whole answer to the request, and nobody downstream sets
		// it, so the tracker is told here. Leaving it unset would record the
		// request as status 0, which reads as a request that never got one at all.
		if t := tracked(r); t != nil {
			t.status = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("<!doctype html><meta charset=utf-8>" +
			"<title>tailport</title><p>Served at <a href=\"" + clean + "/\">" + clean + "/</a>."))
	})
	if len(observers) == 0 {
		return mux
	}
	return observe(mux, observers...)
}

// Server is an http.Server wired to a handler, with the timeouts a tunnel needs.
func Server(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}
