package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/reqlog"
	"github.com/MandavkarPranjal/tailport/internal/state"
)

// saveWatchRun records a run the way a sharing process would, so watch has
// something to find. The pid is the test process, so the record looks alive.
func saveWatchRun(t *testing.T, dir string, port int) state.Run {
	t.Helper()
	run := state.Run{
		PID:      os.Getpid(),
		Port:     port,
		Hostname: "tailport",
		Mode:     state.ModeFunnel,
		Started:  time.Now(),
		URLs:     []string{"https://tailport.example.ts.net"},
	}
	if err := state.Save(dir, run); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	return run
}

// recordRequests leaves the request log a run would have written, so watch has
// something to show.
func recordRequests(t *testing.T, dir string, run state.Run, events ...reqlog.Event) {
	t.Helper()
	w, err := reqlog.OpenWriter(reqlog.Path(dir, run.PID))
	if err != nil {
		t.Fatalf("OpenWriter() error: %v", err)
	}
	for _, ev := range events {
		if err := w.Append(ev); err != nil {
			t.Fatalf("Append() error: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

// stoppedContext ends the watch before it starts, so the command reads the log
// it already has and returns instead of blocking on a live feed.
func stoppedContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

func TestWatchWithNothingSharedSaysSoAndPointsAtUp(t *testing.T) {
	env, out, _ := newTestEnv(t)
	if err := run(stoppedContext(t), []string{"watch", "--state-dir", t.TempDir()}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	text := out.String()
	if !contains(text, "Nothing shared") {
		t.Errorf("output = %q, want it to say nothing is being watched", text)
	}
	if !contains(text, "tailport up") {
		t.Errorf("output = %q, want it to say how to start a share", text)
	}
}

func TestWatchNamesAPortThatIsNotShared(t *testing.T) {
	env, out, _ := newTestEnv(t)
	if err := run(stoppedContext(t), []string{"watch", "--state-dir", t.TempDir(), "3000"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	text := out.String()
	if !contains(text, "Port 3000 is not shared") {
		t.Errorf("output = %q, want it to say port 3000 is not shared", text)
	}
	if !contains(text, "tailport status") {
		t.Errorf("output = %q, want it to point at the command that lists shares", text)
	}
}

func TestWatchShowsTheRequestsTheRunRecorded(t *testing.T) {
	dir := t.TempDir()
	share := saveWatchRun(t, dir, 3000)
	recordRequests(t, dir, share,
		reqlog.Event{Method: "GET", Path: "/index.html", Status: 200, Latency: 3 * time.Millisecond},
		reqlog.Event{Method: "GET", Path: "/missing", Status: 404, Latency: time.Millisecond},
	)

	env, out, _ := newTestEnv(t)
	if err := run(stoppedContext(t), []string{"watch", "--state-dir", dir}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	text := out.String()
	for _, want := range []string{"/index.html", "200", "/missing", "404", "3.0ms"} {
		if !contains(text, want) {
			t.Errorf("watch output does not mention %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Errorf("output = %q, want plain lines when it is not a terminal", text)
	}
}

func TestWatchKeepsOnlyTheNewestRequestsItWasAskedFor(t *testing.T) {
	dir := t.TempDir()
	share := saveWatchRun(t, dir, 3000)
	recordRequests(t, dir, share,
		reqlog.Event{Method: "GET", Path: "/oldest", Status: 200},
		reqlog.Event{Method: "GET", Path: "/middle", Status: 200},
		reqlog.Event{Method: "GET", Path: "/newest", Status: 200},
	)

	env, out, _ := newTestEnv(t)
	if err := run(stoppedContext(t), []string{"watch", "--state-dir", dir, "--lines", "1"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	text := out.String()
	if !contains(text, "/newest") {
		t.Errorf("output = %q, want the newest request", text)
	}
	if contains(text, "/oldest") || contains(text, "/middle") {
		t.Errorf("output = %q, want only the one request asked for", text)
	}
}

func TestWatchShowsNoHistoryAtAllWhenAskedForNoLines(t *testing.T) {
	dir := t.TempDir()
	share := saveWatchRun(t, dir, 3000)
	recordRequests(t, dir, share, reqlog.Event{Method: "GET", Path: "/already-served", Status: 200})

	env, out, _ := newTestEnv(t)
	if err := run(stoppedContext(t), []string{"watch", "--state-dir", dir, "-n", "0"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	// A share that was quiet while somebody set up can be watched for what comes
	// next without being flooded with what already happened.
	if got := out.String(); got != "" {
		t.Errorf("output = %q, want nothing shown before the first live request", got)
	}
}

func TestWatchSaysWhenTheShareHasAlreadyExited(t *testing.T) {
	dir := t.TempDir()
	// A dead run keeps its log, which is the only way there is anything to watch
	// after the sharing process is gone.
	dead := state.Run{PID: 4000000, Port: 3000, Hostname: "tailport", Started: time.Now()}
	if err := state.Save(dir, dead); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	recordRequests(t, dir, dead, reqlog.Event{Method: "GET", Path: "/last", Status: 200})

	env, out, errOut := newTestEnv(t)
	if err := run(stoppedContext(t), []string{"watch", "--state-dir", dir, "3000"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	if !contains(errOut.String(), "has exited") {
		t.Errorf("stderr = %q, want it to say the share is over so the view is not mistaken for live", errOut.String())
	}
	if !contains(out.String(), "/last") {
		t.Errorf("output = %q, want the requests it recorded anyway", out.String())
	}
}

func TestWatchPicksTheRunSharingThePortItWasGiven(t *testing.T) {
	dir := t.TempDir()
	saveWatchRun(t, dir, 3000)
	wanted := saveWatchRun(t, dir, 4000)
	recordRequests(t, dir, wanted, reqlog.Event{Method: "GET", Path: "/the-other-one", Status: 200})

	env, out, _ := newTestEnv(t)
	if err := run(stoppedContext(t), []string{"watch", "--state-dir", dir, "4000"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	if !contains(out.String(), "/the-other-one") {
		t.Errorf("output = %q, want the requests of the named port", out.String())
	}
}

func TestWatchRejectsANonNumericPort(t *testing.T) {
	env, _, _ := newTestEnv(t)
	err := run(stoppedContext(t), []string{"watch", "--state-dir", t.TempDir(), "http"}, env)

	if !errors.Is(err, errUsage) {
		t.Errorf("run() error = %v, want a usage error", err)
	}
	// The argument is echoed back, because the user is more likely to have meant
	// a port than anything the wording could guess.
	if !contains(err.Error(), `"http" is not a port number`) {
		t.Errorf("error = %q, want it to say the argument is not a port", err)
	}
}

func TestWatchRejectsMoreThanOnePort(t *testing.T) {
	env, _, _ := newTestEnv(t)
	err := run(stoppedContext(t), []string{"watch", "--state-dir", t.TempDir(), "3000", "4000"}, env)

	if !errors.Is(err, errUsage) {
		t.Errorf("run() error = %v, want a usage error", err)
	}
}

func TestWatchReportsAStateDirectoryItCannotRead(t *testing.T) {
	env, _, _ := newTestEnv(t)
	file := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	if err := run(stoppedContext(t), []string{"watch", "--state-dir", file}, env); err == nil {
		t.Error("run() error = nil, want the unreadable state directory reported")
	}
}
