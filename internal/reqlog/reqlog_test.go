package reqlog

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendAndReadAllRoundTripEveryField(t *testing.T) {
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

	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll(%q) error: %v", path, err)
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
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
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
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
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

func TestReadAllTreatsAMissingLogAsNothingToReport(t *testing.T) {
	got, err := ReadAll(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll error = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("events = %+v, want nil", got)
	}
}

func TestReadAllSkipsLinesItCannotDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.jsonl")
	if err := os.WriteFile(path, []byte("not json\n{\"method\":\"GET\",\"path\":\"/\",\"status\":204,\"latency\":1000}\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if len(got) != 1 || got[0].Status != 204 || got[0].Latency != time.Microsecond {
		t.Errorf("events = %+v, want the one decodable request", got)
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
	got, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if len(got) != workers {
		t.Fatalf("read %d events, want %d", len(got), workers)
	}
}
