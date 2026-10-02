package proxy

import (
	"context"
	"net/http"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/reqlog"
)

// Observer is told about every request the proxy serves, once the outcome is
// known. It runs on the server goroutine that served the request, before the
// handler returns and so before a small response body is flushed.
//
// That makes it a latency question, not just a locking one: whatever an observer
// costs is added to what the client waits for. An observer must therefore be safe
// to call from several goroutines at once and must not block, and it must not
// write to disk itself. reqlog.Writer.Observe is the observer tailport supplies,
// and it hands the event to a queue instead of writing it, so recording a request
// costs a channel send rather than an operating system call.
type Observer func(reqlog.Event)

// trackerKey is the context key the per-request record is filed under. It is an
// unexported empty struct so no other package can collide with it.
type trackerKey struct{}

// tracker is one request's outcome, filled in by the proxy as it learns it.
//
// It travels in the request context rather than through the ResponseWriter
// because wrapping the writer is how status codes are usually captured, and a
// wrapper has to re-expose Flush, Hijack and ReaderFrom by hand or streaming
// responses, server-sent events and websockets stop working. Keeping the outcome
// out of the writer means watching a share cannot change what it serves.
type tracker struct {
	// status is the status code the client was answered with.
	status int
	// err is the upstream failure, empty when the client got an answer.
	err string
}

// withTracker returns a request carrying an empty tracker.
func withTracker(r *http.Request) (*http.Request, *tracker) {
	t := &tracker{}
	return r.WithContext(context.WithValue(r.Context(), trackerKey{}, t)), t
}

// tracked finds the tracker on a request the proxy is handling. The request the
// ReverseProxy hands to ModifyResponse and ErrorHandler is a clone of the
// inbound one that keeps its context, so the tracker set before the call is
// still reachable from there. It returns nil when there is no tracker, which is
// the case when nothing is watching.
func tracked(r *http.Request) *tracker {
	t, _ := r.Context().Value(trackerKey{}).(*tracker)
	return t
}

// observe times every request passing through next and reports the result to
// each observer in turn.
//
// It is the outermost handler, so the latency it reports covers the whole round
// trip from accepting the request to finishing the response, which is the number
// the person watching a share actually cares about: the tunnel is what they
// added, so it is their latency.
func observe(next http.Handler, observers ...Observer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, t := withTracker(r)
		start := time.Now()

		// The publish is deferred so a request that panics on its way out is
		// still recorded. Such a request reports status 0, which is honest: no
		// status ever reached the client.
		defer func() {
			event := reqlog.Event{
				Time:    time.Now(),
				Method:  req.Method,
				Path:    req.URL.Path,
				Status:  t.status,
				Latency: time.Since(start),
				Error:   t.err,
			}
			for _, o := range observers {
				if o != nil {
					o(event)
				}
			}
		}()

		next.ServeHTTP(w, req)
	})
}
