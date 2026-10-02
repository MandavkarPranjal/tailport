package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/reqlog"
	"github.com/MandavkarPranjal/tailport/internal/state"
)

// saveWatchRun records a run the way a sharing process would, so watch has
// something to find. The record is named after the pid, so two runs sharing one
// pid would overwrite each other and watch would only ever see the last one.
func saveWatchRun(t *testing.T, dir string, pid, port int) state.Run {
	t.Helper()
	run := state.Run{
		PID:      pid,
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

// livePIDs hands out a different live pid on every call, so a test can save more
// than one share in one state directory and watch really has to choose between
// them. They have to be alive because watch reports a share that has exited.
func livePIDs(t *testing.T) func() int {
	t.Helper()
	next := os.Getpid()
	return func() int {
		t.Helper()
		// Pids are handed out in sequence, so the ones just above this process
		// are the likeliest to already exist. A short walk is enough; anything
		// longer means the machine is too odd to guess about.
		for range 64 {
			next++
			if state.Alive(next) {
				return next
			}
		}
		t.Skip("cannot find another live pid on this machine")
		return 0
	}
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
	share := saveWatchRun(t, dir, os.Getpid(), 3000)
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
	share := saveWatchRun(t, dir, os.Getpid(), 3000)
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
	share := saveWatchRun(t, dir, os.Getpid(), 3000)
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
	pids := livePIDs(t)
	other := saveWatchRun(t, dir, pids(), 3000)
	wanted := saveWatchRun(t, dir, pids(), 4000)
	recordRequests(t, dir, wanted, reqlog.Event{Method: "GET", Path: "/the-named-one", Status: 200})
	recordRequests(t, dir, other, reqlog.Event{Method: "GET", Path: "/the-other-one", Status: 200})

	// Two shares at once, so watch has to pick the right log rather than take
	// whichever it happens to read first.
	saved, err := state.List(dir)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(saved) != 2 {
		t.Fatalf("List() returned %d runs, want both shares on disk", len(saved))
	}

	env, out, _ := newTestEnv(t)
	if err := run(stoppedContext(t), []string{"watch", "--state-dir", dir, "4000"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}

	text := out.String()
	if !contains(text, "/the-named-one") {
		t.Errorf("output = %q, want the requests of the named port", text)
	}
	if contains(text, "/the-other-one") {
		t.Errorf("output = %q, want the other share left out", text)
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

func TestWatchShowsEveryRequestAShareServesWhileItIsWatching(t *testing.T) {
	dir := t.TempDir()
	share := saveWatchRun(t, dir, os.Getpid(), 3000)
	recordRequests(t, dir, share, reqlog.Event{Method: "GET", Path: "/before-watch", Status: 200})

	w, err := reqlog.OpenWriter(reqlog.Path(dir, share.PID))
	if err != nil {
		t.Fatalf("OpenWriter error: %v", err)
	}

	// A busy share is exactly when somebody watches, and it is serving the whole
	// time watch is reading its history and starting to follow it. Every one of
	// these has to be shown once: a request falling between the two halves of a
	// watch would be shown by neither.
	const served = 300
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range served {
			if err := w.Append(reqlog.Event{
				Method: "GET",
				Path:   fmt.Sprintf("/served-%03d", i),
				Status: 200,
			}); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(t.Context())
	env, out, _ := newTestEnv(t)
	finished := make(chan error, 1)
	go func() {
		finished <- run(ctx, []string{"watch", "--state-dir", dir, "-n", "1000"}, env)
	}()

	// Wait for the last request to be shown, then stop.
	last := fmt.Sprintf("/served-%03d", served-1)
	deadline := time.After(10 * time.Second)
	for !strings.Contains(out.String(), last) {
		select {
		case <-deadline:
			t.Fatalf("watch never showed %s:\n%s", last, out.String())
		case err := <-finished:
			t.Fatalf("watch returned early: %v\n%s", err, out.String())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()
	<-done
	if err := w.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	<-finished

	text := out.String()
	for i := range served {
		path := fmt.Sprintf("/served-%03d", i)
		if n := strings.Count(text, path); n != 1 {
			t.Errorf("%s appeared %d times, want exactly once", path, n)
		}
	}
}
