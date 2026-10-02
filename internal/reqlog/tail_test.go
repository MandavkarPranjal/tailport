package reqlog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fastPoll keeps the tests quick without making them flaky; the tailer works on
// a real clock, it just does not need to idle for a tenth of a second.
const fastPoll = time.Millisecond

func TestTailPublishesWhatIsAppendedAfterItStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	defer w.Close()

	tail := Start(path, Options{Poll: fastPoll})
	ctx, cancel := context.WithCancel(t.Context())
	done := follow(t, tail, ctx)

	// Give the follower a chance to open the log and settle on its offset before
	// anything is written, so this is really testing "from now on".
	time.Sleep(20 * fastPoll)
	want := Event{Method: "POST", Path: "/live", Status: 201, Latency: 7 * time.Millisecond}
	if err := w.Append(want); err != nil {
		t.Fatalf("Append error: %v", err)
	}

	got := next(t, tail)
	if got.Method != want.Method || got.Path != want.Path || got.Status != want.Status || got.Latency != want.Latency {
		t.Errorf("event = %+v, want %+v", got, want)
	}
	stop(t, cancel, done)
	if tail.Dropped() != 0 {
		t.Errorf("Dropped() = %d, want 0", tail.Dropped())
	}
}

func TestTailSkipsWhatTheLogAlreadyHeldUnlessItIsAskedToReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.requests.jsonl")
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	if err := w.Append(Event{Method: "GET", Path: "/old", Status: 200}); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	// A live watcher wants what happens from now on, not a replay of history it
	// has already been shown.
	later := Start(path, Options{Poll: fastPoll})
	ctx, cancel := context.WithCancel(t.Context())
	done := follow(t, later, ctx)
	time.Sleep(20 * fastPoll)
	earlier := nextOrTimeout(t, later, 50*fastPoll)
	if earlier != nil {
		t.Fatalf("event = %+v, want none: the old line was already there", earlier)
	}
	stop(t, cancel, done)

	replay := Start(path, Options{FromStart: true, Poll: fastPoll})
	ctx2, cancel2 := context.WithCancel(t.Context())
	done2 := follow(t, replay, ctx2)
	got := next(t, replay)
	if got.Path != "/old" {
		t.Errorf("event path = %q, want /old", got.Path)
	}
	stop(t, cancel2, done2)
}

func TestTailWaitsForALogThatDoesNotExistYet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "later.requests.jsonl")
	tail := Start(path, Options{Poll: fastPoll})
	ctx, cancel := context.WithCancel(t.Context())
	done := follow(t, tail, ctx)

	if got := nextOrTimeout(t, tail, 20*fastPoll); got != nil {
		t.Fatalf("event = %+v, want none while the run has not started", got)
	}
	// The run starts after the watcher is already waiting, which is the case
	// `tailport watch` before `tailport up` depends on.
	w, err := OpenWriter(path)
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}
	defer w.Close()
	if err := w.Append(Event{Method: "GET", Path: "/first", Status: 200}); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	if got := next(t, tail); got.Path != "/first" {
		t.Errorf("event path = %q, want /first", got.Path)
	}
	stop(t, cancel, done)
}

func TestTailPublishesARecordWrittenInTwoPiecesOnlyOnceItIsWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.requests.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create log: %v", err)
	}
	tail := Start(path, Options{Poll: fastPoll})
	ctx, cancel := context.WithCancel(t.Context())
	done := follow(t, tail, ctx)
	time.Sleep(20 * fastPoll)

	line := `{"method":"GET","path":"/slow-write","status":200,"latency":5000000}` + "\n"
	half := len(line) / 2
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.WriteString(line[:half]); err != nil {
		t.Fatalf("write first half: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after first half: %v", err)
	}

	if got := nextOrTimeout(t, tail, 30*fastPoll); got != nil {
		t.Fatalf("event = %+v, want none: the record is only half written", got)
	}
	f, err = os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("reopen log: %v", err)
	}
	if _, err := f.WriteString(line[half:]); err != nil {
		t.Fatalf("write second half: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after second half: %v", err)
	}

	got := next(t, tail)
	if got.Path != "/slow-write" {
		t.Errorf("event path = %q, want /slow-write", got.Path)
	}
	if extra := nextOrTimeout(t, tail, 30*fastPoll); extra != nil {
		t.Errorf("second event = %+v, want none: the halves must not be published twice", extra)
	}
	stop(t, cancel, done)
}

func TestTailStartsOverWhenTheLogIsTruncated(t *testing.T) {
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
	tail := Start(path, Options{FromStart: true, Poll: fastPoll})
	ctx, cancel := context.WithCancel(t.Context())
	done := follow(t, tail, ctx)
	if got := next(t, tail); got.Path != "/before" {
		t.Fatalf("event path = %q, want /before", got.Path)
	}

	// Truncating is what a reused pid does, so the follower has to notice rather
	// than sit at an offset past the end of the new file forever.
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("truncate log: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"method":"GET","path":"/after","status":204,"latency":1000}`+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite log: %v", err)
	}
	got := next(t, tail)
	if got.Path != "/after" {
		t.Errorf("event path = %q, want /after after the truncation", got.Path)
	}
	stop(t, cancel, done)
}

func TestTailDropsEventsRatherThanBlockingTheRun(t *testing.T) {
	tail := &Tail{events: make(chan Event, 2), poll: fastPoll, path: filepath.Join(t.TempDir(), "absent.jsonl")}
	// Nobody is reading, so the buffer fills and then has to start refusing.
	for i := range 10 {
		tail.publish(Event{Path: "/" + string(rune('a'+i))})
	}
	if tail.Dropped() != 8 {
		t.Errorf("Dropped() = %d, want 8", tail.Dropped())
	}
	// The events already buffered are still readable, so a follower that was
	// briefly busy does not lose what it had already been handed.
	if len(tail.events) != 2 {
		t.Errorf("buffered = %d, want 2", len(tail.events))
	}
}

func TestFollowClosesTheEventChannelWhenTheContextEnds(t *testing.T) {
	tail := Start(filepath.Join(t.TempDir(), "absent.jsonl"), Options{Poll: fastPoll})
	ctx, cancel := context.WithCancel(t.Context())
	done := follow(t, tail, ctx)
	cancel()
	select {
	case _, ok := <-tail.Events():
		if ok {
			t.Error("event channel yielded an event, want closed")
		}
	case <-time.After(time.Second):
		t.Fatal("event channel was not closed within a second of cancelling")
	}
	<-done
}

func TestStartDoesNotFailOnAMissingLog(t *testing.T) {
	tail := Start(filepath.Join(t.TempDir(), "no-such-dir", "r.requests.jsonl"), Options{})
	if tail == nil {
		t.Fatal("Start returned nil, want a usable Tail")
	}
	if got := tail.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d, want 0", got)
	}
}

// follow runs Follow in the background and returns a channel closed when it
// returns, so a test can wait for the follower to finish before it checks the
// final state.
func follow(t *testing.T, tail *Tail, ctx context.Context) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tail.Follow(ctx)
	}()
	return done
}

// next waits for one event, failing the test if none turns up.
func next(t *testing.T, tail *Tail) Event {
	t.Helper()
	select {
	case e, ok := <-tail.Events():
		if !ok {
			t.Fatal("event channel closed before an event arrived")
		}
		return e
	case <-time.After(time.Second):
		t.Fatal("no event arrived within a second")
		return Event{}
	}
}

// nextOrTimeout waits briefly for an event and reports nil when the tail stays
// quiet, which is how a test asserts that nothing was published.
func nextOrTimeout(t *testing.T, tail *Tail, wait time.Duration) *Event {
	t.Helper()
	select {
	case e, ok := <-tail.Events():
		if !ok {
			return nil
		}
		return &e
	case <-time.After(wait):
		return nil
	}
}

// stop cancels the follower and waits for it to return, so the test can then
// check state that only settles once the follower is finished.
func stop(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Follow did not return within a second")
	}
}
