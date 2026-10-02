package reqlog

import (
	"bytes"
	"context"
	"io"
	"os"
	"time"
)

// pollInterval is how often a follower looks for more lines. There is no
// portable way to wait on a file being appended to, so the tailer polls; at a
// tenth of a second a live dashboard is indistinguishable from an event driven
// one, and a handful of runs cost almost nothing.
const pollInterval = 100 * time.Millisecond

// eventBuffer is how many events may queue up for a follower that has not read
// them yet. A follower that falls this far behind drops the newest event rather
// than stalling: a live view that shows slightly less is better than one that
// gets further and further behind, since the whole point is to show now.
const eventBuffer = 512

// Tail follows a request log that another process is still writing to. It is
// what lets `tailport watch` show a run somebody else started: the run owns the
// file, and a watcher only reads it.
type Tail struct {
	events chan Event
	// path is the log being followed.
	path string
	// buf holds a line that has not been terminated yet, so a record the
	// writer is in the middle of is not mistaken for a corrupt one.
	buf []byte
	// read is the offset of the end of the last whole line consumed.
	read int64
	// file is the open log, or nil when there is nothing to read yet.
	file *os.File
	// poll is how long to wait between passes.
	poll time.Duration
	// dropped counts events discarded because the follower fell behind, so the
	// view can say so instead of quietly lying about being complete.
	dropped int
}

// Options tunes a Tail.
type Options struct {
	// FromStart replays the lines the log already holds before following it. A
	// live watcher wants only what happens from now on.
	FromStart bool
	// Poll overrides the poll interval, which tests use to avoid waiting.
	Poll time.Duration
}

// Start opens a log for following. A log that does not exist yet is not an
// error: the run may not have served a first request, or may not have started,
// and a follower has to keep waiting for the file to appear either way.
func Start(path string, opts Options) *Tail {
	t := &Tail{
		events: make(chan Event, eventBuffer),
		path:   path,
		poll:   opts.Poll,
	}
	if t.poll <= 0 {
		t.poll = pollInterval
	}
	if info, err := os.Stat(path); err == nil {
		t.read = info.Size()
		if opts.FromStart {
			t.read = 0
		}
	}
	return t
}

// Events is the channel new events arrive on. It is closed once Follow returns,
// so a caller ranging over it stops on its own.
func (t *Tail) Events() <-chan Event { return t.events }

// Follow reads new lines until ctx ends, publishing each event on the Events
// channel and closing it on the way out. It runs until ctx is cancelled: a run
// that dies simply stops writing, and a watcher that gave up at that point would
// be useless for the case that matters most, which is watching a share that has
// not been started yet.
func (t *Tail) Follow(ctx context.Context) {
	defer close(t.events)
	defer t.close()

	ticker := time.NewTicker(t.poll)
	defer ticker.Stop()

	for {
		// The first pass happens immediately, so an opening screen is not held
		// back by a whole poll interval.
		t.drain()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Dropped is how many events were discarded because the follower could not keep
// up. It is read after Follow returns, so a caller that wants the number should
// take it at the end.
func (t *Tail) Dropped() int { return t.dropped }

func (t *Tail) close() {
	if t.file != nil {
		_ = t.file.Close()
		t.file = nil
	}
}

// drain consumes whatever whole lines the log has grown since the last pass.
func (t *Tail) drain() {
	if t.file == nil {
		f, err := os.Open(t.path)
		if err != nil {
			// The run has not created its log yet, or is between runs. Waiting
			// is the right answer, not an error.
			return
		}
		t.file = f
	}

	info, err := t.file.Stat()
	if err != nil {
		t.close()
		return
	}
	size := info.Size()
	if size < t.read {
		// The log shrank, so the run behind this pid is a different one and the
		// offset no longer means what it did. Start again from the top instead
		// of decoding bytes from the middle of somebody else's records.
		t.close()
		t.read, t.buf = 0, nil
		return
	}
	if size == t.read {
		return
	}

	if _, err := t.file.Seek(t.read, io.SeekStart); err != nil {
		t.close()
		return
	}
	// Read no further than the length this stat reported, so a line the writer
	// is still writing stays in the buffer until its newline arrives.
	chunk := make([]byte, size-t.read)
	n, err := t.file.Read(chunk)
	if n > 0 {
		t.consume(chunk[:n])
		t.read += int64(n)
	}
	if err != nil && err != io.EOF {
		t.close()
	}
}

// consume splits freshly read bytes into whole lines and publishes each one. A
// trailing fragment is kept in t.buf, so a record written in two pieces is
// published once, whole, rather than twice or half decoded.
func (t *Tail) consume(chunk []byte) {
	data := append(t.buf, chunk...)
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if e, ok := decode(data[:i]); ok {
			t.publish(e)
		}
		data = data[i+1:]
	}
	t.buf = data
}

// publish hands an event to the follower, dropping it if the follower is not
// keeping up. Blocking here would make the view lag further and further behind
// the run, which is the one thing a live watch must not do.
func (t *Tail) publish(e Event) {
	select {
	case t.events <- e:
	default:
		t.dropped++
	}
}
