package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArgsAlwaysCarriesTheEssentials(t *testing.T) {
	opts := Options{Port: 3000, Hostname: "tailport", StateDir: "/state"}
	got := opts.Args()
	want := []string{ServeCommand, "-port", "3000", "-hostname", "tailport", "-state-dir", "/state"}
	if !slices.Equal(got, want) {
		t.Errorf("Args() = %v, want %v", got, want)
	}
}

func TestArgsAddsOptionalFlagsOnlyWhenSet(t *testing.T) {
	full := Options{
		Port:      8080,
		Path:      "/demo",
		Hostname:  "tailport",
		StateDir:  "/state",
		LogPath:   "/state/logs/tailport-8080.log",
		All:       true,
		Private:   true,
		Ephemeral: true,
		AuthKey:   "tskey-secret",
	}
	got := strings.Join(full.Args(), " ")
	for _, want := range []string{
		"-path /demo",
		"-log /state/logs/tailport-8080.log",
		"-all",
		"-private",
		"-ephemeral",
		"-authkey tskey-secret",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Args() = %q, want it to contain %q", got, want)
		}
	}

	bare := Options{Port: 1, Hostname: "tailport", StateDir: "/state"}
	for _, unwanted := range []string{"-path", "-log", "-all", "-private", "-ephemeral", "-authkey"} {
		if strings.Contains(strings.Join(bare.Args(), " "), unwanted) {
			t.Errorf("Args() = %v, want no %s", bare.Args(), unwanted)
		}
	}
}

func TestIsDetached(t *testing.T) {
	t.Setenv(DetachedEnv, "")
	if IsDetached() {
		t.Error("IsDetached() = true without the env var")
	}
	t.Setenv(DetachedEnv, "1")
	if !IsDetached() {
		t.Error("IsDetached() = false with the env var set")
	}
}

func TestLogFileName(t *testing.T) {
	got := LogFileName("/state", "tailport", 3000)
	want := filepath.Join("/state", "logs", "tailport-3000.log")
	if got != want {
		t.Errorf("LogFileName() = %q, want %q", got, want)
	}
	if got := LogFileName("/state", "", 3000); filepath.Base(got) != "tailport-3000.log" {
		t.Errorf("LogFileName() with no hostname = %q, want the default name", got)
	}
}

func TestWaitForFileReturnsWhenTheFileAppears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ready")
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(path, []byte("ok"), 0o600)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitForFile(ctx, path); err != nil {
		t.Errorf("WaitForFile() error: %v", err)
	}
}

func TestWaitForFileHonoursTheContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := WaitForFile(ctx, filepath.Join(t.TempDir(), "never"))
	if err == nil {
		t.Fatal("WaitForFile() = nil, want the context error")
	}
}

func TestWaitForFileReturnsImmediatelyForAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "there")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WaitForFile(ctx, path); err != nil {
		t.Errorf("WaitForFile() error: %v", err)
	}
}

func TestStartRunsTheSameBinaryInTheBackground(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "child.log")

	// Start re-executes this test binary, so the child has to leave immediately
	// before it runs any test. TestMain does that when it sees childEnv.
	t.Setenv(childEnv, "1")

	pid, err := Start(Options{
		Port:     3000,
		Hostname: "tailport",
		StateDir: dir,
		LogPath:  logPath,
	})
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if pid <= 0 {
		t.Errorf("Start() returned pid %d, want a real pid", pid)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Errorf("log file was not created: %v", err)
	}
}

// childEnv marks the re-executed test binary so it exits instead of testing.
const childEnv = "TAILPORT_DAEMON_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStartFailsWithoutALogPath(t *testing.T) {
	_, err := Start(Options{Port: 3000, LogPath: filepath.Join(t.TempDir(), "missing-dir", "x.log")})
	if err == nil {
		t.Fatal("Start() with an unwritable log path = nil error, want an error")
	}
}
