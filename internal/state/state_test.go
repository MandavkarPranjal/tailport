package state

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sample(pid, port int, started time.Time) Run {
	return Run{
		PID:      pid,
		Port:     port,
		Path:     "/demo",
		Hostname: "tailport",
		Mode:     ModeFunnel,
		Started:  started,
		URLs:     []string{"http://tailport", "https://tailport.example.ts.net"},
		LogPath:  "/tmp/tailport.log",
	}
}

func TestFileName(t *testing.T) {
	if got, want := FileName(4242), "4242.json"; got != want {
		t.Errorf("FileName() = %q, want %q", got, want)
	}
}

func TestDir(t *testing.T) {
	if got, want := Dir("/home/u/.config/tailport"), filepath.Join("/home/u/.config/tailport", "runs"); got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := sample(os.Getpid(), 3000, time.Now().Round(time.Second))

	if err := Save(dir, want); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	got, err := Load(dir, want.PID)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got.Port != want.Port || got.Hostname != want.Hostname || got.Mode != want.Mode {
		t.Errorf("Load() = %+v, want the saved run %+v", got, want)
	}
	if !got.Started.Equal(want.Started) {
		t.Errorf("Started = %v, want %v", got.Started, want.Started)
	}
	if len(got.URLs) != len(want.URLs) || got.URLs[1] != want.URLs[1] {
		t.Errorf("URLs = %v, want %v", got.URLs, want.URLs)
	}
}

func TestSaveUsesPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	run := sample(os.Getpid(), 3000, time.Now())
	if err := Save(dir, run); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	info, err := os.Stat(filepath.Join(Dir(dir), FileName(run.PID)))
	if err != nil {
		t.Fatalf("Stat() error: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
}

func TestLoadMissingRun(t *testing.T) {
	if _, err := Load(t.TempDir(), 1234); err == nil {
		t.Fatal("Load() on a missing file = nil error, want an error")
	}
}

func TestDecodeRejectsIncompleteRecords(t *testing.T) {
	tests := map[string]string{
		"no pid":       `{"port":3000}`,
		"no port":      `{"pid":10}`,
		"not json":     `{`,
		"empty object": `{}`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decode([]byte(data)); err == nil {
				t.Errorf("decode(%s) = nil error, want an error", data)
			}
		})
	}
}

func TestListIsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	newer := sample(2, 3001, base.Add(time.Minute))
	older := sample(1, 3000, base)
	for _, run := range []Run{newer, older} {
		if err := Save(dir, run); err != nil {
			t.Fatalf("Save() error: %v", err)
		}
	}
	runs, err := List(dir)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("List() returned %d runs, want 2", len(runs))
	}
	if runs[0].PID != older.PID {
		t.Errorf("first run pid = %d, want %d", runs[0].PID, older.PID)
	}
}

func TestListSkipsUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, sample(7, 3000, time.Now())); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	runsDir := Dir(dir)
	for _, name := range []string{"broken.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(runsDir, name), []byte("garbage"), 0o600); err != nil {
			t.Fatalf("WriteFile() error: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(runsDir, "sub.json"), 0o700); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}

	runs, err := List(dir)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(runs) != 1 || runs[0].PID != 7 {
		t.Errorf("List() = %+v, want only the saved run", runs)
	}
}

func TestListOnMissingDirectory(t *testing.T) {
	runs, err := List(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if runs != nil {
		t.Errorf("List() = %+v, want nil", runs)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	run := sample(11, 3000, time.Now())
	if err := Save(dir, run); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if err := Remove(dir, run.PID); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if _, err := Load(dir, run.PID); err == nil {
		t.Error("record still readable after Remove()")
	}
	if err := Remove(dir, run.PID); err != nil {
		t.Errorf("Remove() twice = %v, want nil", err)
	}
}

func TestPruneKeepsLiveRunsAndDropsDeadOnes(t *testing.T) {
	dir := t.TempDir()
	live := sample(os.Getpid(), 3000, time.Now())
	dead := sample(deadPID(t), 3001, time.Now().Add(-time.Hour))
	for _, run := range []Run{live, dead} {
		if err := Save(dir, run); err != nil {
			t.Fatalf("Save() error: %v", err)
		}
	}

	removed, err := Prune(dir)
	if err != nil {
		t.Fatalf("Prune() error: %v", err)
	}
	if removed != 1 {
		t.Errorf("Prune() removed %d files, want 1", removed)
	}
	if _, err := Load(dir, dead.PID); err == nil {
		t.Error("dead run record survived Prune()")
	}
	if _, err := Load(dir, live.PID); err != nil {
		t.Errorf("live run record was removed: %v", err)
	}
}

// A run killed rather than stopped leaves its request log behind, and nothing
// else will ever clear it, so Prune has to. The log sits beside the record that
// says whether its run is still alive, so the decision has to be made while that
// record is still readable.
func TestPruneRemovesTheRequestLogOfADeadRun(t *testing.T) {
	dir := t.TempDir()
	dead := sample(deadPID(t), 3001, time.Now().Add(-time.Hour))
	if err := Save(dir, dead); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	log := filepath.Join(Dir(dir), fmt.Sprintf("%d%s", dead.PID, requestLogSuffix))
	if err := os.WriteFile(log, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	if _, err := Prune(dir); err != nil {
		t.Fatalf("Prune() error: %v", err)
	}

	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("the request log of a dead run survived Prune(), stat error = %v", err)
	}
}

// A live share is still serving, so its log is still wanted even though Prune
// walks right past its record.
func TestPruneKeepsTheRequestLogOfALiveRun(t *testing.T) {
	dir := t.TempDir()
	live := sample(os.Getpid(), 3000, time.Now())
	if err := Save(dir, live); err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	log := filepath.Join(Dir(dir), fmt.Sprintf("%d%s", live.PID, requestLogSuffix))
	if err := os.WriteFile(log, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	if _, err := Prune(dir); err != nil {
		t.Fatalf("Prune() error: %v", err)
	}

	if _, err := os.Stat(log); err != nil {
		t.Errorf("the request log of a live run was removed: %v", err)
	}
}

func TestPruneOnMissingDirectory(t *testing.T) {
	removed, err := Prune(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("Prune() error: %v", err)
	}
	if removed != 0 {
		t.Errorf("Prune() removed %d, want 0", removed)
	}
}

func TestAliveOnThisProcess(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("Alive() on this process = false, want true")
	}
}

func TestAliveOnADeadProcess(t *testing.T) {
	if Alive(deadPID(t)) {
		t.Error("Alive() on a finished process = true, want false")
	}
}

// deadPID returns a pid that is known to be gone. Pids are handed out in
// sequence, so a pid well above the current one is unused on any sane machine.
func deadPID(t *testing.T) int {
	t.Helper()
	pid := 4000000
	if Alive(pid) {
		t.Skip("cannot find an unused pid on this machine")
	}
	return pid
}
