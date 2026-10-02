package reqlog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	// Shut the real drain goroutine off and take the queue by hand, so what is on
	// disk is decided by this test rather than by whichever goroutine got there
	// first. Asserting an empty log while a drain is racing to write it would
	// fail against a correct implementation.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("OpenFile error: %v", err)
	}
	detached := &Writer{file: file, queue: make(chan Event, queueDepth)}

	detached.Observe(Event{Method: "GET", Path: "/on-the-serving-goroutine", Status: 200})

	// Observe runs on the goroutine that just answered a request, before the
	// handler returns and so before a small body is flushed. If it wrote the
	// record itself, this share's disk would be added to every client's latency.
	if got, _, err := ReadTail(path, allEvents); err != nil {
		t.Fatalf("ReadTail error: %v", err)
	} else if len(got) != 0 {
		t.Errorf("events = %+v, want Observe to hand the record off instead of writing it", got)
	}

	// Handing off is only safe because the record does land, so the goroutine
	// that takes the queue has to be the one to write it.
	close(detached.queue)
	for e := range detached.queue {
		if err := detached.write(e); err != nil {
			t.Fatalf("write error: %v", err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	got, _, err := ReadTail(path, allEvents)
	if err != nil {
		t.Fatalf("ReadTail error: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/on-the-serving-goroutine" {
		t.Errorf("events = %+v, want the handed off request written once", got)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
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

// quietWriter is a Writer with no drain goroutine, so a test decides exactly when
// records land and can assert what is and is not on disk yet. The real
// OpenWriter is still what creates the file, so the path and the permissions are
// the real ones.
func quietWriter(t *testing.T, depth int) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter() error: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return &Writer{file: w.file, queue: make(chan Event, depth), drained: make(chan struct{})}, path
}

// drainQuietly runs the drain by hand, the way Close expects to find it running.
func drainQuietly(t *testing.T, w *Writer) {
	t.Helper()
	go w.drain()
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

// logAt reads every record a log ends up holding. A zero limit would ask for no
// records at all, which is the right answer for a watcher and the wrong one here.
func logAt(t *testing.T, path string) []Event {
	t.Helper()
	events, _, err := ReadTail(path, 1000)
	if err != nil {
		t.Fatalf("ReadTail() error: %v", err)
	}
	return events
}

// noticesIn counts the announcements a log made about its own gaps.
func noticesIn(events []Event) []string {
	var out []string
	for _, ev := range events {
		if ev.Notice != "" {
			out = append(out, ev.Notice)
		}
	}
	return out
}

func TestAFailedWriteIsReportedWhenTheLogCloses(t *testing.T) {
	w, _ := quietWriter(t, 4)
	w.queue <- Event{Method: "GET", Path: "/a", Status: 200}

	// Close the file behind the writer's back. This is the shape of the problem:
	// the writer is open and believes it is serving, and the disk underneath it
	// has stopped taking anything. Whoever asked for the request must not find
	// out, but whoever closed the run has to.
	if err := w.file.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	go w.drain()
	err := w.Close()

	if err == nil {
		t.Fatal("Close() = nil, want the write that could not land reported")
	}
	if !strings.Contains(err.Error(), "write request log") {
		t.Errorf("Close() = %q, want it to name the write that failed", err)
	}
}

func TestALogThatWroteEverythingClosesWithoutComplaint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter() error: %v", err)
	}
	w.Observe(Event{Method: "GET", Path: "/a", Status: 200})

	if err := w.Close(); err != nil {
		t.Errorf("Close() = %v, want nil when every record landed", err)
	}
	if got := logAt(t, path); len(got) != 1 {
		t.Errorf("the log holds %d records, want the one that was observed", len(got))
	}
}

func TestDropsAreWrittenIntoTheLogSoAWatcherCanSeeThem(t *testing.T) {
	w, path := quietWriter(t, 2)

	// Two fit and the next two cannot, which is the shape of a share serving
	// faster than its disk can record.
	for i := range 4 {
		w.Observe(Event{Method: "GET", Path: fmt.Sprintf("/%d", i), Status: 200})
	}
	if got := w.Dropped(); got != 2 {
		t.Fatalf("Dropped() = %d, want 2 events dropped", got)
	}

	drainQuietly(t, w)

	events := logAt(t, path)
	if got := len(noticesIn(events)); got != 1 {
		t.Fatalf("the log holds %d notices, want 1", got)
	}
	// The notice lands where the writer caught up rather than at the end of the
	// log, so it is found by looking for it. That is the point of it: the gap is
	// marked where it is, not described later.
	var notice Event
	for _, ev := range events {
		if ev.Notice != "" {
			notice = ev
		}
	}
	if want := "2 requests were not recorded"; notice.Notice != want {
		t.Errorf("Notice = %q, want %q", notice.Notice, want)
	}
	// A notice is about the log, so it must not look like a request in the log.
	if notice.Method != "" || notice.Path != "" || notice.Status != 0 {
		t.Errorf("the notice looks like a request: %+v", notice)
	}
}

func TestDropsAreAnnouncedOnceAndThenAgainOnlyWhenThereAreMore(t *testing.T) {
	w, path := quietWriter(t, 1)

	w.Observe(Event{Method: "GET", Status: 200})
	w.Observe(Event{Method: "GET", Status: 200}) // dropped
	w.noteDrops(w.dropped.Load())
	w.noteDrops(w.dropped.Load()) // nothing new to say

	drainQuietly(t, w)

	if got := noticesIn(logAt(t, path)); len(got) != 1 {
		t.Errorf("the log holds %d notices, want 1 for a single batch of drops", len(got))
	}
}

func TestASingleDroppedRequestIsAnnouncedInTheSingular(t *testing.T) {
	w, path := quietWriter(t, 1)
	w.Observe(Event{Method: "GET", Status: 200})
	w.Observe(Event{Method: "GET", Status: 200}) // dropped

	drainQuietly(t, w)

	if got := logAt(t, path); len(got) == 0 {
		t.Fatal("the log holds nothing, want the notice for what it lost")
	} else if want := "1 request was not recorded"; got[len(got)-1].Notice != want {
		t.Errorf("Notice = %q, want %q", got[len(got)-1].Notice, want)
	}
}

func TestDropsAreAnnouncedEvenWhenNothingElseIsLeftToWrite(t *testing.T) {
	w, path := quietWriter(t, 1)

	// One event fits and the other two cannot, so nothing is left in the queue
	// for the drain to notice a gap through.
	for range 3 {
		w.Observe(Event{Method: "GET", Status: 200})
	}

	drainQuietly(t, w)

	got := noticesIn(logAt(t, path))
	if len(got) == 0 {
		t.Fatal("the log holds no notice, want it to admit what it lost as it closed")
	}
	if want := "2 requests were not recorded"; got[len(got)-1] != want {
		t.Errorf("Notice = %q, want %q", got[len(got)-1], want)
	}
}

func TestDropsAreCountedRunningRatherThanPerBatch(t *testing.T) {
	w, path := quietWriter(t, 1)
	for range 2 {
		w.Observe(Event{Method: "GET", Status: 200}) // dropped
	}
	w.noteDrops(w.dropped.Load())
	w.Observe(Event{Method: "GET", Status: 200})
	w.Observe(Event{Method: "GET", Status: 200}) // dropped
	w.noteDrops(w.dropped.Load())

	drainQuietly(t, w)

	got := noticesIn(logAt(t, path))
	if len(got) != 2 {
		t.Fatalf("the log holds %d notices, want one per batch", len(got))
	}
	// A gap in a log reads as "everything up to here is all of it", so the second
	// notice has to carry the total so far. The second batch alone was two, so a
	// notice saying two would leave the reader thinking nothing was lost before.
	if want := "3 requests were not recorded"; got[1] != want {
		t.Errorf("second Notice = %q, want %q", got[1], want)
	}
}

func TestANoticeNeverCountsAsARequestInTheDashboard(t *testing.T) {
	// The record has to survive the JSON round trip a watcher makes of it, since
	// that is how every notice reaches one.
	w, path := quietWriter(t, 1)
	w.Observe(Event{Method: "GET", Status: 200})
	w.Observe(Event{Method: "GET", Status: 200}) // dropped

	drainQuietly(t, w)

	for _, ev := range logAt(t, path) {
		if ev.Notice != "" {
			if ev.Status != 0 || ev.Method != "" || ev.Path != "" || ev.Latency != 0 {
				t.Errorf("the notice carries request fields: %+v", ev)
			}
		}
	}
}

func TestASustainedOverloadCostsOneNoticeRatherThanOnePerRequest(t *testing.T) {
	const served = 25
	// A queue deep enough to hold the whole burst, so nothing is lost while it is
	// being set up. The drops happen later, while the writer is working through
	// it, which is the shape of a share serving faster than its disk can record.
	w, path := quietWriter(t, served)
	for range served {
		w.Observe(Event{Method: "GET", Path: "/queued", Status: 200})
	}

	// Work through the queue by hand, always refilling it so it never empties:
	// every write lands on a gap that grew since the last one, and none of them
	// is the end of the burst.
	for i := range served {
		w.drainEvent(<-w.queue)
		w.Observe(Event{Method: "GET", Path: fmt.Sprintf("/%d", i), Status: 200})
		w.Observe(Event{Method: "GET", Path: "/lost", Status: 200}) // dropped
	}
	// Hand the rest to the real drain, which is what Close is waiting for.
	go w.drain()
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if got := w.Dropped(); got != served {
		t.Fatalf("Dropped() = %d, want %d", got, served)
	}

	events := logAt(t, path)

	// One line per served request would roughly double the log and fill a piped
	// watcher with near-identical notices, all saying the same standing
	// condition. Close accounts for the burst once, which is what answers "how
	// much is missing".
	if got := noticesIn(events); len(got) != 1 {
		t.Fatalf("the log holds %d notices %q, want 1 for a burst that never let the writer catch up", len(got), got)
	}
	if want := "25 requests were not recorded"; noticesIn(events)[0] != want {
		t.Errorf("Notice = %q, want %q", noticesIn(events)[0], want)
	}
	if got, want := len(events), 2*served+1; got != want {
		t.Errorf("the log holds %d records, want %d requests and one notice", got, want)
	}
}

func TestAGapThatEndsIsAnnouncedWhereTheWriterCaughtUp(t *testing.T) {
	// Two fit, two are lost, and then the writer catches up: the notice belongs
	// after the last request that made it in, which is where the reader looking
	// for the hole will find it.
	w, path := quietWriter(t, 2)
	for i := range 4 {
		w.Observe(Event{Method: "GET", Path: fmt.Sprintf("/%d", i), Status: 200})
	}

	drainQuietly(t, w)

	events := logAt(t, path)
	if got := len(noticesIn(events)); got != 1 {
		t.Fatalf("the log holds %d notices, want 1", got)
	}
	if want := "2 requests were not recorded"; noticesIn(events)[0] != want {
		t.Errorf("Notice = %q, want %q", noticesIn(events)[0], want)
	}
	if last := events[len(events)-1]; last.Notice == "" {
		t.Errorf("the last record is %+v, want the notice after the gap it marks", last)
	}
}

// seqIn reads the request number back out of a path the producer above wrote.
func seqIn(path string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(path, "/r"))
	if err != nil {
		return -1
	}
	return n
}

// TestANoticeNeverLandsAheadOfARequestThatWasAcceptedBeforeTheDrops pins where a
// notice may sit in the log rather than what it says.
//
// Worth being honest about what this does and does not do. The ordering it
// guards could not be made to happen by any schedule tried here, including
// several thousand contended bursts, because reaching it needs an accepted
// request and a dropped one to land inside the handful of nanoseconds between
// the writer's two reads of shared state, and one Observe call is already longer
// than that. An earlier version of this test did fail against the unfixed
// writer, but every failure was the harmless case: a running total written
// beside a request that was accepted after the drop it counts, which is true and
// says nothing. So this test passes against the old ordering too. It is here to
// state the invariant rather than to prove a bug that is hard to reach.
//
// One producer submits requests in order, so which ones were dropped and which
// survived is known exactly: only Observe grows the drop count and only this
// goroutine calls Observe, so reading Dropped either side of one call says
// whether that request was accepted without racing anything. Each burst is a
// pair, one that fits in the queue and one that does not, and a pause before it
// lets the writer catch up, which is the moment a notice is written at all.
func TestANoticeNeverLandsAheadOfARequestThatWasAcceptedBeforeTheDrops(t *testing.T) {
	w, path := quietWriter(t, 1)
	go w.drain()

	// A queue of one and a tight loop, which is the shape most likely to have the
	// writer repeatedly seen caught up while requests are still going wrong.
	const pairs = 6000
	var dropped []int
	seq := 0
	for range pairs {
		for range 2 {
			before := w.Dropped()
			w.Observe(Event{Method: "GET", Path: fmt.Sprintf("/r%05d", seq)})
			if w.Dropped() != before {
				dropped = append(dropped, seq)
			}
			seq++
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	if len(dropped) == 0 {
		t.Skip("the writer kept up with every request, so there was no gap to describe")
	}

	// A notice marks where the writer caught up, and the count it carries is the
	// running total. So every request accepted before the drop that total was
	// taken at has to be written before the notice; requests accepted after that
	// drop may sit on either side, since the notice says nothing about them.
	//
	// The queue is first in first out, so requests appear in the order they were
	// accepted. A notice is therefore owed satisfaction by the first request after
	// it that was numbered at or above the drop it describes.
	var owed []int
	for _, ev := range logAt(t, path) {
		if ev.Notice != "" {
			count, _, _ := strings.Cut(ev.Notice, " ")
			total, err := strconv.Atoi(count)
			if err != nil || total < 1 || total > len(dropped) {
				t.Fatalf("notice %q names a count this test cannot account for, out of %d drops", ev.Notice, len(dropped))
			}
			owed = append(owed, dropped[total-1])
			continue
		}
		seq := seqIn(ev.Path)
		for i := 0; i < len(owed); {
			if seq >= owed[i] {
				owed = append(owed[:i], owed[i+1:]...)
				continue
			}
			t.Errorf("request %d was accepted before the drop that the notice above describes, but is written after it: the gap is marked ahead of a request that survived it", seq)
			i++
		}
	}
}
