package reqlog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// allEvents is a count large enough that reading a test's whole log is the
// tail, so a test that cares about every record need not know how many there are.
const allEvents = 1000

func TestAppendAndReadTailRoundTripEveryField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter(%q) error: %v", path, err)
	}
	events := []Event{
		{Time: time.Now(), Method: "GET", Path: "/", Status: 200, Latency: 3 * time.Millisecond},
		{Time: time.Now(), Method: "POST", Path: "/api/thing", Status: 502, Latency: time.Second, Error: "connection refused"},
	}
	for _, e := range events {
		if err := w.Append(e); err != nil {
			t.Fatalf("Append(%+v) error: %v", e, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	got, _, err := ReadTail(path, len(events))
	if err != nil {
		t.Fatalf("ReadTail(%q) error: %v", path, err)
	}
	if len(got) != len(events) {
		t.Fatalf("read %d events, want %d", len(got), len(events))
	}
	for i, want := range events {
		if got[i].Method != want.Method || got[i].Path != want.Path {
			t.Errorf("event %d = %s %s, want %s %s", i, got[i].Method, got[i].Path, want.Method, want.Path)
		}
		if got[i].Status != want.Status {
			t.Errorf("event %d status = %d, want %d", i, got[i].Status, want.Status)
		}
		if got[i].Latency != want.Latency {
			t.Errorf("event %d latency = %v, want %v", i, got[i].Latency, want.Latency)
		}
		if got[i].Error != want.Error {
			t.Errorf("event %d error = %q, want %q", i, got[i].Error, want.Error)
		}
		if !got[i].Time.Equal(want.Time) {
			t.Errorf("event %d time = %v, want %v", i, got[i].Time, want.Time)
		}
	}
}

func TestAppendIsReadableBeforeTheLogIsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter(%q) error: %v", path, err)
	}
	defer w.Close()

	// A watcher following a running share reads the log as the requests happen.
	// If records waited in a buffer, the live view would stay blank for as long
	// as the share stayed quiet, which is the moment somebody is watching it.
	if err := w.Append(Event{Method: "GET", Path: "/live", Status: 200}); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	got, _, err := ReadTail(path, allEvents)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/live" {
		t.Errorf("events = %+v, want the request while the writer is still open", got)
	}
}

func TestOpenWriterCreatesTheStateDirectoryAndTruncates(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir, 4242)
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter(%q) error: %v", path, err)
	}
	if err := w.Append(Event{Method: "GET", Path: "/old", Status: 200}); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if !strings.HasSuffix(FileName(4242), ".requests.jsonl") {
		t.Errorf("FileName(4242) = %q, want a .requests.jsonl suffix", FileName(4242))
	}

	// A second writer for the same pid truncates, because the record is keyed by
	// pid and a reused pid would otherwise replay the previous run's requests.
	w2, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	defer w2.Close()
	if err := w2.Append(Event{Method: "GET", Path: "/new", Status: 200}); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	got, _, err := ReadTail(path, allEvents)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/new" {
		t.Errorf("events = %+v, want only the new request", got)
	}
}

func TestCloseIsSafeTwiceAndAppendAfterCloseFails(t *testing.T) {
	w, err := OpenWriter(filepath.Join(t.TempDir(), "r.jsonl"))
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close error = %v, want nil", err)
	}
	if err := w.Append(Event{Method: "GET"}); err == nil {
		t.Error("Append after Close returned nil, want an error")
	}
}

func TestObserveDropsTheWriteErrorInsteadOfFailingTheRequest(t *testing.T) {
	w, err := OpenWriter(filepath.Join(t.TempDir(), "r.jsonl"))
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	// Observe has no error return, so this is a compile-time fact: a broken log
	// cannot take a request down with it.
	w.Observe(Event{Method: "GET", Path: "/", Status: 200})
}

func TestRemoveDeletesTheLogAndToleratesAMissingOne(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir, 99)
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if err := Remove(dir, 99); err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("stat after Remove = %v, want not exist", err)
	}
	if err := Remove(dir, 99); err != nil {
		t.Errorf("second Remove error = %v, want nil", err)
	}
}

func TestReadTailTreatsAMissingLogAsNothingToReport(t *testing.T) {
	got, _, err := ReadTail(filepath.Join(t.TempDir(), "absent.jsonl"), 20)
	if err != nil {
		t.Fatalf("ReadTail error = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("events = %+v, want nil", got)
	}
}

func TestReadTailSkipsLinesItCannotDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.jsonl")
	if err := os.WriteFile(path, []byte("not json\n{\"method\":\"GET\",\"path\":\"/\",\"status\":204,\"latency\":1000}\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	got, _, err := ReadTail(path, allEvents)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}
	if len(got) != 1 || got[0].Status != 204 || got[0].Latency != time.Microsecond {
		t.Errorf("events = %+v, want the one decodable request", got)
	}
}

func TestReadTailKeepsOnlyTheNewestRequestsItWasAskedFor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	for i := range 50 {
		if err := w.Append(Event{Method: "GET", Path: fmt.Sprintf("/p%d", i), Status: 200}); err != nil {
			t.Fatalf("Append error: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	got, _, err := ReadTail(path, 3)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}

	// A share can have been serving all day, and a screenful is all anybody
	// watches. Reading the whole log to show three rows would make the watcher's
	// memory grow with the run rather than with the screen.
	if len(got) != 3 {
		t.Fatalf("read %d events, want only the 3 newest", len(got))
	}
	for i, want := range []string{"/p47", "/p48", "/p49"} {
		if got[i].Path != want {
			t.Errorf("event %d path = %q, want %q", i, got[i].Path, want)
		}
	}
}

func TestReadTailReturnsNothingWhenNoHistoryWasAskedFor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	if err := w.Append(Event{Method: "GET", Path: "/already-served", Status: 200}); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// `watch -n 0` is somebody who only wants to see what happens next, and
	// saying so is different from showing a stale screenful.
	for _, n := range []int{0, -1} {
		got, _, err := ReadTail(path, n)
		if err != nil {
			t.Fatalf("ReadTail(_, %d) error: %v", n, err)
		}
		if len(got) != 0 {
			t.Errorf("ReadTail(_, %d) = %+v, want nothing", n, got)
		}
	}
}

func TestReadTailStillCountsUndecodableLinesTowardsNothing(t *testing.T) {
	// A log with more noise than requests must still yield the newest requests,
	// so skipping a line must not shrink the tail.
	path := filepath.Join(t.TempDir(), "r.requests.jsonl")
	var sb strings.Builder
	for i := range 10 {
		fmt.Fprintf(&sb, "garbage %d\n", i)
		e, err := json.Marshal(Event{Method: "GET", Path: fmt.Sprintf("/p%d", i), Status: 200})
		if err != nil {
			t.Fatalf("Marshal error: %v", err)
		}
		sb.Write(e)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	got, _, err := ReadTail(path, 2)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}

	if len(got) != 2 || got[0].Path != "/p8" || got[1].Path != "/p9" {
		t.Errorf("events = %+v, want the 2 newest requests", got)
	}
}

func TestReadTailReadsARequestWhosePathIsAsLongAsNetHTTPAllows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	// net/http accepts a request line up to DefaultMaxHeaderBytes, and every
	// byte of it can come back out of JSON as a six byte escape. A share really
	// can be asked for a path this long, so reading its log must not fail.
	long := "/" + strings.Repeat("<", http.DefaultMaxHeaderBytes)
	if err := w.Append(Event{Method: "GET", Path: long, Status: 200}); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	// Confirm the line really is past the old 1MiB limit, so this test cannot
	// quietly stop covering the case it was written for.
	line, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if len(line) <= 1<<20 {
		t.Fatalf("line is %d bytes, want it past the old 1MiB limit for this test to mean anything", len(line))
	}

	got, _, err := ReadTail(path, 1)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}
	if len(got) != 1 || got[0].Path != long {
		t.Errorf("read %d events, want the long request back", len(got))
	}
}

func TestReadTailKeepsGoingPastALineNoRequestCouldHaveProduced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	if err := w.Append(Event{Method: "GET", Path: "/before", Status: 200}); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	// A line past maxLine is not something tailport wrote: the log was corrupted
	// or truncated by something else. Whatever was readable before it is still
	// worth showing, and one bad record must not end a watch that was working.
	oversized := strings.Repeat("x", maxLine+1) + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("OpenFile error: %v", err)
	}
	if _, err := f.WriteString(oversized); err != nil {
		t.Fatalf("write oversized line: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, _, err := ReadTail(path, 10)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/before" {
		t.Errorf("events = %+v, want the readable request kept", got)
	}
}

func TestAppendFromManyGoroutinesKeepsEveryLineWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	const workers = 20
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Append(Event{Method: "GET", Path: "/" + string(rune('a'+i)), Status: 200})
		}()
	}
	wg.Wait()
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	got, _, err := ReadTail(path, allEvents)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}
	if len(got) != workers {
		t.Fatalf("read %d events, want %d", len(got), workers)
	}
}

func TestObserveDoesNotWriteOnTheCallersGoroutine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}

	w.Observe(Event{Method: "GET", Path: "/on-the-serving-goroutine", Status: 200})

	// Observe runs on the goroutine that just answered a request, before the
	// handler returns and so before a small body is flushed. If it wrote the
	// record itself, this share's disk would be added to every client's latency.
	if got, _, err := ReadTail(path, allEvents); err != nil {
		t.Fatalf("ReadTail error: %v", err)
	} else if len(got) != 0 {
		t.Errorf("events = %+v, want Observe to hand the record off instead of writing it", got)
	}
}

func TestCloseWaitsForWhatObserveQueued(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}

	// Handing the record off means a run that shuts down a moment later still has
	// to leave the requests it served behind, not lose the tail of its own log.
	for i := range 50 {
		w.Observe(Event{Method: "GET", Path: fmt.Sprintf("/q%d", i), Status: 200})
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	got, _, err := ReadTail(path, allEvents)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}
	if len(got) != 50 {
		t.Errorf("events = %d, want all 50 written before Close returned", len(got))
	}
}

func TestObserveDropsRatherThanBlockingWhenTheQueueIsFull(t *testing.T) {
	// A writer with no goroutine taking from its queue, which is the state a full
	// queue is in: nothing drains while the share is serving.
	w := &Writer{queue: make(chan Event, 2)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 10 {
			w.Observe(Event{Method: "GET", Path: fmt.Sprintf("/f%d", i)})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Observe blocked on a full queue")
	}

	if got := w.Dropped(); got != 8 {
		t.Errorf("Dropped() = %d, want 8", got)
	}
}

func TestDroppedCountsNothingWhenTheQueueKeptUp(t *testing.T) {
	w, err := OpenWriter(filepath.Join(t.TempDir(), "run.requests.jsonl"))
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	defer w.Close()

	for range 100 {
		w.Observe(Event{Method: "GET", Path: "/"})
	}

	if got := w.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d, want 0 for a queue that kept up", got)
	}
}
