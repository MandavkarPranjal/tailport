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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	// Notice is a message from the run about its own log rather than a request
	// it served, such as how many requests it had to drop. Only one record at a
	// time carries one, and a watcher shows it apart from the requests so it is
	// never mistaken for one or counted as one.
	Notice string `json:"notice,omitempty"`
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
// Records reach the file as soon as a goroutine can write them, rather than
// sitting in a buffer, because the whole point of the log is that a watcher
// following it sees requests as they happen. Batching eight kilobytes of records
// would keep a live view blank for as long as a share stayed quiet, which is
// exactly when somebody is watching to see whether anything is wrong.
type Writer struct {
	mu   sync.Mutex
	file *os.File
	// stopped is set by Close: no further events are accepted and the queue is
	// shut. The file itself is closed by the drain goroutine, but only once it
	// has written everything already queued, so nothing accepted is ever lost.
	stopped bool
	// closed records that the file is closed, which happens strictly after
	// stopped. Append reports errClosed rather than an operating system error, so
	// a request still in flight when a run shuts down hears why.
	closed bool
	// queue is what Observe hands events to, and drained is closed once the
	// goroutine taking from it has finished.
	queue   chan Event
	drained chan struct{}
	// writeErr is the first thing that went wrong while recording the log:
	// either a queued write that could not land, or the close that failed if
	// nothing went wrong before it. It is written before drained is closed, so
	// Close can read it safely once the drain has finished.
	writeErr error
	// dropped counts the events Observe had to throw away because the queue was
	// full.
	dropped atomic.Int64
	// reported is how many of those drops the log has already told its readers
	// about. Only the drain goroutine touches it.
	reported int64
}

// queueDepth is how many events may be waiting to be written before the log
// starts dropping them. It is a few times what a screen shows, so a burst costs
// the writer nothing, and bounded, so that a share serving faster than the disk
// can record cannot grow its queue until it takes the service down with it.
const queueDepth = 1024

// OpenWriter prepares a run's request log for appending, creating the state
// directory if it needs to. An existing log is truncated: it is keyed by pid, so
// anything already there belongs to a run that reused this pid and is not worth
// replaying.
//
// It starts a goroutine to write what Observe queues, and Close is what stops it.
func OpenWriter(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open request log: %w", err)
	}
	w := &Writer{
		file:    file,
		queue:   make(chan Event, queueDepth),
		drained: make(chan struct{}),
	}
	go w.drain()
	return w, nil
}

// drain writes queued events one at a time, and then closes the file. Doing both
// here is what lets Close be one step: shut the queue and wait for it.
//
// A failed write is kept rather than thrown away. It cannot be reported to
// anybody at the time it happens, since nothing is watching the goroutine that
// is serving requests, but Close is waiting for this goroutine and can hand it
// on. Only the first failure is kept, because that is the one that explains the
// rest: once a disk is full every write after it fails for the same reason.
func (w *Writer) drain() {
	defer close(w.drained)
	for e := range w.queue {
		if err := w.write(e); err != nil && w.writeErr == nil {
			w.writeErr = err
		}
		w.noteDrops()
	}
	// Drops can arrive with nothing left to write, so the log has to account for
	// them as it closes too.
	w.noteDrops()
	if err := w.closeFile(); err != nil && w.writeErr == nil {
		w.writeErr = err
	}
}

// write puts one event on disk in a single write, so a follower sees it as soon
// as it lands.
func (w *Writer) write(e Event) error {
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

// Append records one event before returning, so a caller that needs the record on
// disk has it. Prefer Observe on anything answering a request.
func (w *Writer) Append(e Event) error {
	w.mu.Lock()
	stopped := w.stopped
	w.mu.Unlock()
	if stopped {
		return errClosed
	}
	return w.write(e)
}

// Observe is Append shaped to be handed to a proxy as an observer.
//
// It hands the event to a goroutine and returns, because it runs on the goroutine
// that just finished answering a request: a client waiting on a share should not
// be waiting on this share's disk either. A log that cannot be written is not
// worth failing a request the service already answered, so the only consequence
// of losing one is a gap in what `tailport watch` can show.
//
// The queue is bounded, and a full queue drops the newest event rather than
// blocking. Dropping is the right way round: a request log is a record of what a
// share was doing, and holding up the requests being served to keep that record
// complete would trade away the thing the share exists to do. Dropped() says how
// many were lost, so a log can admit it is short instead of quietly lying about
// how much it saw.
func (w *Writer) Observe(e Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	select {
	case w.queue <- e:
	default:
		w.dropped.Add(1)
	}
}

// Dropped reports how many events Observe had to throw away because the queue was
// full. It is safe to ask while a run is being served.
func (w *Writer) Dropped() int {
	return int(w.dropped.Load())
}

// noteDrops writes a notice into the log when events have been dropped since the
// last notice, so somebody watching learns about the gap from the log they are
// already reading rather than only from the share's own output, which they may
// not be able to see at all.
//
// The notice carries the running total rather than the latest batch, because a
// gap in a log is read as "everything up to here is all of it". It is written
// from the drain goroutine, which is the only writer, so a busy share pays for it
// with a counter read and at most one extra line rather than with anything on the
// goroutines answering requests.
func (w *Writer) noteDrops() {
	dropped := w.dropped.Load()
	if dropped == w.reported {
		return
	}
	w.reported = dropped

	what := "requests were not recorded"
	if dropped == 1 {
		what = "request was not recorded"
	}
	// The notice is itself a record in the log, so it can fail to be written.
	// There is nothing to be done about that here: the count is already in
	// Dropped, and a run that cannot write this line cannot write the next one
	// either.
	_ = w.write(Event{Notice: fmt.Sprintf("%d %s", dropped, what)})
}

// closeFile closes the file once, and reports what happened if it had to.
func (w *Writer) closeFile() error {
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

// Close stops accepting events, waits for the ones already queued to be written,
// and then closes the file. Calling it twice is safe, so a deferred Close next to
// an explicit one does not panic.
//
// It reports the first thing that went wrong along the way, so a share can say
// out loud that its log was short rather than leaving a watcher to assume it saw
// everything.
func (w *Writer) Close() error {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return nil
	}
	w.stopped = true
	close(w.queue)
	w.mu.Unlock()

	// Waiting for the drain to finish is what keeps the log honest: a share that
	// shuts down must not lose the requests it served on its way out.
	<-w.drained
	return w.writeErr
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

// maxLine is the longest request log line ReadTail will consider.
//
// A request path is not short by construction: net/http accepts a request line
// up to DefaultMaxHeaderBytes, and encoding/json can turn any one byte of it
// into a six character escape on the way out. A control byte becomes \uXXXX, and
// <, > and & are escaped as well, so a log stays safe to embed in a page.
// A valid request therefore writes a line up to roughly six times the header
// limit, so a token limit below that would fail the read on a request tailport
// really did serve and abort a watch that was working. Rounding up leaves room
// for the other fields and for the growth those bounds leave behind.
//
// A line past this is treated as unreadable rather than fatal, the same as a
// line that will not parse, so one corrupt record cannot end a watch.
const maxLine = 8 * http.DefaultMaxHeaderBytes

// ReadTail returns the last n events already written to a log, oldest first, and
// the byte offset it read up to. A count of zero or less asks for no events at
// all, which is what somebody watching only what happens from now on wants.
//
// The offset is the other half of the result because reading the past and
// following the future are two halves of one job, and only a reader that knows
// where it stopped can be handed to a follower without a gap. A caller that has
// just read a log should start the tail at this offset rather than at the end of
// the file: a request appended between the two would be past the end the tail
// assumes and behind the read the tail is meant to continue, so it would appear
// in neither. Pass the offset to Start as where to begin following.
//
// Only the tail is kept, because a log belongs to a run that may have been
// serving for hours: a share left up over a busy afternoon records far more
// requests than any screen shows, and reading all of them would make the memory
// a watcher needs grow with the log rather than with what it displays. The file
// is still scanned end to end, since the newest records are at the end, but
// nothing beyond the tail is ever held.
//
// A line that does not parse is skipped rather than failing the read, because a
// partially written final line is normal while a run is still serving.
func ReadTail(path string, n int) ([]Event, int64, error) {
	if n <= 0 {
		return nil, lengthOf(path), nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("open request log: %w", err)
	}
	defer file.Close()

	// The tail is held in a ring of exactly n, so a run with more requests than
	// that costs one memmove per line instead of an ever-growing slice.
	events := make([]Event, 0, n)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 8<<10), maxLine)
	for scanner.Scan() {
		e, ok := decode(scanner.Bytes())
		if !ok {
			continue
		}
		if len(events) == n {
			copy(events, events[1:])
			events[n-1] = e
			continue
		}
		events = append(events, e)
	}
	// Where the file was left is the other half of the answer, so a follower can
	// pick up from here rather than from wherever the log has got to by the time
	// it starts. The scanner stops on the last whole line it read, which is
	// exactly the boundary a record appended after this moment belongs past.
	at, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return events, 0, fmt.Errorf("read request log offset: %w", err)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			// The log holds a line no request could have produced, so it is
			// corrupt or was truncated by something else. Whatever was readable
			// before it is still worth showing, and a watcher is more useful than
			// a complaint. The offset is left at zero, so a tail resuming from it
			// replays the readable part rather than skipping it.
			return events, 0, nil
		}
		return events, 0, fmt.Errorf("read request log: %w", err)
	}
	return events, at, nil
}

// lengthOf is how long a log is right now, for the caller that wants no events
// from it but still needs to know where the log ends. A log that does not exist
// is zero, which is also where a follower should wait for it.
func lengthOf(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// decode parses one line, reporting false for a line that is not a whole event.
func decode(line []byte) (Event, bool) {
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return Event{}, false
	}
	return e, true
}
