// Package reqlog is the request log a tailport run leaves behind, so a second
// process can watch what a share is serving. Every served run appends one JSON
// object per line to a file named after its pid, next to its state record, and
// `tailport watch` follows those lines to draw a live view.
package reqlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/state"
)

// Event is one request that reached a run's proxy, recorded once the outcome is
// known. It carries the few things a person watching a share wants to see: what
// was asked for, how it was answered, and how long it took.
type Event struct {
	// Time is when the request finished. The proxy timestamps on completion so
	// the log stays honest about ordering even when requests overlap.
	Time time.Time `json:"time"`
	// Method is the HTTP method, e.g. GET.
	Method string `json:"method"`
	// Path is the request path as the client asked for it, before any rewriting
	// or prefix stripping, since that is what the person watching recognises.
	Path string `json:"path"`
	// Status is the status code the client was answered with, or 0 when the
	// request never got that far.
	Status int `json:"status"`
	// Latency is how long the request took, in nanoseconds on disk. It is
	// encoded as a number because time.Duration is an integer type; readers
	// get it back as a duration.
	Latency time.Duration `json:"latency"`
	// Error is the upstream failure, when the local service could not be
	// reached at all. It is empty for a request that got any answer.
	Error string `json:"error,omitempty"`
}

// FileName is the request log of one run, keyed by pid like a state record. The
// .jsonl suffix keeps it out of state.List and state.Prune, which only look at
// .json files, so a request log is never mistaken for a run record.
func FileName(pid int) string {
	return fmt.Sprintf("%d.requests.jsonl", pid)
}

// Path is where a run's request log lives, next to its state record.
func Path(stateDir string, pid int) string {
	return filepath.Join(state.Dir(stateDir), FileName(pid))
}

// Writer appends events to a run's request log. It is safe to call from every
// server goroutine at once.
//
// Records go to the file immediately rather than through a buffer, because the
// whole point of the log is that a watcher following it sees requests as they
// happen. Batching eight kilobytes of records would keep a live view blank for
// as long as a share stayed quiet, which is exactly when somebody is watching to
// see whether anything is wrong.
type Writer struct {
	mu     sync.Mutex
	file   *os.File
	closed bool
}

// OpenWriter prepares a run's request log for appending, creating the state
// directory if it needs to. An existing log is truncated: it is keyed by pid, so
// anything already there belongs to a run that reused this pid and is not worth
// replaying.
func OpenWriter(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open request log: %w", err)
	}
	return &Writer{file: file}, nil
}

// Append records one event. The line is written straight through in a single
// write, so a follower sees it as soon as it lands.
func (w *Writer) Append(e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode request log entry: %w", err)
	}
	data = append(data, '\n')

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errClosed
	}
	if _, err := w.file.Write(data); err != nil {
		return fmt.Errorf("write request log: %w", err)
	}
	return nil
}

// Observe is Append shaped to be handed to a proxy as an observer. A log that
// cannot be written is not worth failing a request the service already answered,
// so the error is dropped: the request is served either way, and the only
// consequence is a gap in what `tailport watch` can show.
func (w *Writer) Observe(e Event) {
	_ = w.Append(e)
}

// Close closes the file. Calling it twice is safe, so a deferred Close next to
// an explicit one does not panic.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close request log: %w", err)
	}
	return nil
}

// errClosed marks an append to a log that has already been closed, which
// happens to a request still in flight when the run shuts down.
var errClosed = errors.New("request log is closed")

// Remove deletes the request log of a pid. A run unlinks its own log when it
// stops, so this exists for the cases where it could not, such as a machine that
// lost power and left a record whose process no longer exists.
func Remove(stateDir string, pid int) error {
	err := os.Remove(Path(stateDir, pid))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove request log: %w", err)
	}
	return nil
}

// ReadAll returns every event already written to a log. A line that does not
// parse is skipped rather than failing the read, because a partially written
// final line is normal while a run is still serving.
func ReadAll(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open request log: %w", err)
	}
	defer file.Close()

	var events []Event
	scanner := bufio.NewScanner(file)
	// A request line is small, but a path can be long and there is no reason to
	// cap it at the default 64KiB.
	scanner.Buffer(make([]byte, 0, 8<<10), 1<<20)
	for scanner.Scan() {
		if e, ok := decode(scanner.Bytes()); ok {
			events = append(events, e)
		}
	}
	if err := scanner.Err(); err != nil {
		return events, fmt.Errorf("read request log: %w", err)
	}
	return events, nil
}

// decode parses one line, reporting false for a line that is not a whole event.
func decode(line []byte) (Event, bool) {
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return Event{}, false
	}
	return e, true
}
