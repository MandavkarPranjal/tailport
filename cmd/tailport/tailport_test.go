package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/state"
	"github.com/MandavkarPranjal/tailport/internal/ui"
)

// newTestEnv returns an Env wired to buffers so a test can read what was
// printed, and keeps the test off a terminal so nothing prompts.
func newTestEnv(t *testing.T) (*ui.Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	// A buffer is not a terminal, so NewEnv already leaves colour off.
	return ui.NewEnv(strings.NewReader(""), &out, &errOut), &out, &errOut
}

func contains(got, want string) bool { return strings.Contains(got, want) }

func TestVersionPrintsTheVersion(t *testing.T) {
	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"version"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if !contains(out.String(), version) {
		t.Errorf("output = %q, want it to mention version %q", out, version)
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"help"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	text := out.String()
	for _, want := range []string{"tailport up", "tailport status", "tailport stop", "tailport list", "tailport login"} {
		if !contains(text, want) {
			t.Errorf("help does not mention %q", want)
		}
	}
	if !contains(text, "http://tailport") {
		t.Errorf("help does not show the tailnet URL:\n%s", text)
	}
}

func TestHelpFlagIsAccepted(t *testing.T) {
	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"--help"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if !contains(out.String(), "Usage:") {
		t.Error("--help did not print usage")
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	env, _, errOut := newTestEnv(t)
	err := run(t.Context(), []string{"frobnicate"}, env)
	if err == nil {
		t.Fatal("run() = nil, want an error")
	}
	if !errors.Is(err, errUsage) {
		t.Errorf("error %v does not wrap errUsage", err)
	}
	if !contains(errOut.String(), "Usage:") {
		t.Error("usage was not printed after the error")
	}
}

func TestUpRejectsAnUnexpectedArgument(t *testing.T) {
	env, _, _ := newTestEnv(t)
	err := run(t.Context(), []string{"up", "3000"}, env)
	if err == nil || !errors.Is(err, errUsage) {
		t.Fatalf("run() = %v, want a usage error", err)
	}
}

func TestUpRejectsAnInvalidPort(t *testing.T) {
	env, _, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"up", "-p", "70000"}, env); err == nil {
		t.Fatal("run() = nil, want an invalid port error")
	}
}

func TestUpRejectsAnUnpublishablePublicPort(t *testing.T) {
	env, _, _ := newTestEnv(t)
	err := run(t.Context(), []string{"up", "-p", "3000", "--public-port", "3000"}, env)
	if err == nil || !errors.Is(err, errUsage) {
		t.Fatalf("run() = %v, want a usage error naming the publishable ports", err)
	}
	if !contains(err.Error(), "443") {
		t.Errorf("error %q does not list the publishable ports", err)
	}
}

func TestUpAllRejectsAnExplicitPublicPort(t *testing.T) {
	env, _, _ := newTestEnv(t)
	err := run(t.Context(), []string{"up", "-p", "3000", "--all", "--public-port", "8443"}, env)
	if err == nil || !errors.Is(err, errUsage) {
		t.Fatalf("run() = %v, want a usage error", err)
	}
}

func TestUpAllAcceptsTheDefaultPublicPort(t *testing.T) {
	opts := shareOpts{Port: 3000, All: true, PublicPort: 443}
	if err := opts.validate(); err != nil {
		t.Errorf("validate() error: %v", err)
	}
	if got := opts.publicPorts(); len(got) != 3 || got[0] != 443 || got[2] != 10000 {
		t.Errorf("publicPorts() = %v, want every Funnel port", got)
	}
}

func TestUpPrivateSkipsThePublicPortCheck(t *testing.T) {
	opts := shareOpts{Port: 3000, Private: true, PublicPort: 3000}
	if err := opts.validate(); err != nil {
		t.Errorf("validate() error: %v", err)
	}
	if got := opts.publicPorts(); len(got) != 1 || got[0] != 3000 {
		t.Errorf("publicPorts() = %v, want the requested port", got)
	}
}

func TestUpPathMustBeAbsolute(t *testing.T) {
	opts := shareOpts{Port: 3000, Path: "demo", PublicPort: 443}
	err := opts.validate()
	if err == nil || !errors.Is(err, errUsage) {
		t.Fatalf("validate() = %v, want a usage error", err)
	}
	if err := (shareOpts{Port: 3000, Path: "/demo", PublicPort: 443}).validate(); err != nil {
		t.Errorf("validate() with /demo error: %v", err)
	}
}

func TestUpWithoutAPortAndWithoutATerminal(t *testing.T) {
	env, _, _ := newTestEnv(t)
	err := run(t.Context(), []string{"up", "-y"}, env)
	if !errors.Is(err, errNoPortSelected) {
		t.Fatalf("run() = %v, want errNoPortSelected", err)
	}
	if !contains(err.Error(), "-p 3000") {
		t.Errorf("error %q does not show how to pass a port", err)
	}
}

func TestShortFlagsAliasLongOnes(t *testing.T) {
	var port int
	var all, yes, daemonise bool
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.IntVar(&port, "port", 0, "")
	fs.BoolVar(&all, "all", false, "")
	fs.BoolVar(&yes, "yes", false, "")
	fs.BoolVar(&daemonise, "daemon", false, "")
	bindShort(fs, "p", "port")
	bindShort(fs, "a", "all")
	bindShort(fs, "y", "yes")
	bindShort(fs, "d", "daemon")
	if err := fs.Parse([]string{"-p", "3000", "-a", "-y", "-d"}); err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if port != 3000 || !all || !yes || !daemonise {
		t.Errorf("short flags gave port=%d all=%v yes=%v daemon=%v", port, all, yes, daemonise)
	}
}

func TestBindShortIgnoresAnUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	bindShort(fs, "z", "nope") // must not panic
}

func TestParsePort(t *testing.T) {
	if got, err := parsePort(" 3000\n"); err != nil || got != 3000 {
		t.Errorf("parsePort() = %d, %v, want 3000, nil", got, err)
	}
	if _, err := parsePort("http"); err == nil {
		t.Error("parsePort(\"http\") = nil error, want an error")
	}
}

func TestResolvePortPrefersTheFlag(t *testing.T) {
	env, _, _ := newTestEnv(t)
	got, err := resolvePort(env, 8080, false)
	if err != nil || got != 8080 {
		t.Errorf("resolvePort() = %d, %v, want 8080, nil", got, err)
	}
}

func TestResolvePortRefusesToGuessUnderYes(t *testing.T) {
	env, _, _ := newTestEnv(t)
	if _, err := resolvePort(env, 0, true); !errors.Is(err, errNoPortSelected) {
		t.Errorf("resolvePort() = %v, want errNoPortSelected", err)
	}
}

func TestResolvePortRefusesToGuessWithoutATerminal(t *testing.T) {
	env, _, _ := newTestEnv(t)
	if _, err := resolvePort(env, 0, false); !errors.Is(err, errNoPortSelected) {
		t.Errorf("resolvePort() = %v, want errNoPortSelected", err)
	}
}

func TestStatusWithNothingShared(t *testing.T) {
	env, out, _ := newTestEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := run(t.Context(), []string{"status", "--state-dir", t.TempDir()}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if !contains(out.String(), "Nothing shared") {
		t.Errorf("output = %q, want it to say nothing is shared", out)
	}
}

func TestStatusListsARun(t *testing.T) {
	dir := t.TempDir()
	rec := state.Run{
		PID:      os.Getpid(),
		Port:     3000,
		Hostname: "tailport",
		Mode:     state.ModeFunnel,
		Started:  time.Now(),
		URLs:     []string{"http://tailport", "https://tailport.example.ts.net"},
	}
	if err := state.Save(dir, rec); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"status", "--state-dir", dir}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	text := out.String()
	for _, want := range []string{"tailport", "3000", "http://tailport", "https://tailport.example.ts.net"} {
		if !contains(text, want) {
			t.Errorf("status output does not mention %q:\n%s", want, text)
		}
	}
	if contains(text, "stale") {
		t.Errorf("status calls a live run stale:\n%s", text)
	}
}

func TestStatusPrunesDeadRecords(t *testing.T) {
	dir := t.TempDir()
	dead := state.Run{PID: 4000000, Port: 3001, Hostname: "tailport", Started: time.Now()}
	if err := state.Save(dir, dead); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"status", "--state-dir", dir}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if contains(out.String(), "3001") {
		t.Errorf("status kept a dead record:\n%s", out)
	}
	if _, err := state.Load(dir, dead.PID); err == nil {
		t.Error("status did not prune the dead record")
	}
}

func TestStatusAllKeepsStaleRecords(t *testing.T) {
	dir := t.TempDir()
	dead := state.Run{PID: 4000000, Port: 3001, Hostname: "tailport", Started: time.Now()}
	if err := state.Save(dir, dead); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"status", "--state-dir", dir, "--all"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if !contains(out.String(), "3001") {
		t.Errorf("status --all dropped a stale record:\n%s", out)
	}
	if !contains(out.String(), "stale") {
		t.Errorf("status --all does not flag the record as stale:\n%s", out)
	}
}

func TestStatusRejectsAnUnexpectedArgument(t *testing.T) {
	env, _, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"status", "now"}, env); !errors.Is(err, errUsage) {
		t.Errorf("run() = %v, want a usage error", err)
	}
}

func TestStopWithNothingRunning(t *testing.T) {
	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"stop", "--state-dir", t.TempDir()}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if !contains(out.String(), "Nothing is shared") {
		t.Errorf("output = %q, want it to say nothing is shared", out)
	}
}

func TestStopNoticesAPortThatIsNotShared(t *testing.T) {
	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"stop", "--state-dir", t.TempDir(), "3000"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if !contains(out.String(), "Port 3000 is not shared") {
		t.Errorf("output = %q, want it to report port 3000 is not shared", out)
	}
}

func TestStopRejectsANonNumericPort(t *testing.T) {
	env, _, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"stop", "--state-dir", t.TempDir(), "http"}, env); !errors.Is(err, errUsage) {
		t.Errorf("run() = %v, want a usage error", err)
	}
}

func TestStopRemovesTheRecordOfADeadProcess(t *testing.T) {
	dir := t.TempDir()
	dead := state.Run{PID: 4000000, Port: 3000, Hostname: "tailport", Started: time.Now()}
	if err := state.Save(dir, dead); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"stop", "--state-dir", dir}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if !contains(out.String(), "Stopped sharing port 3000") {
		t.Errorf("output = %q, want confirmation", out)
	}
	if _, err := state.Load(dir, dead.PID); err == nil {
		t.Error("record survived stop")
	}
}

func TestListPrintsSomething(t *testing.T) {
	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"list", "-limit", "3"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	text := out.String()
	if !contains(text, "tailport up -p PORT") {
		t.Errorf("list output does not say how to share a port:\n%s", text)
	}
}

func TestListRejectsAnUnexpectedArgument(t *testing.T) {
	env, _, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"list", "everything"}, env); !errors.Is(err, errUsage) {
		t.Errorf("run() = %v, want a usage error", err)
	}
}

func TestUpListDoesNotStartAnything(t *testing.T) {
	env, out, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"up", "--list"}, env); err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if !contains(out.String(), "tailport up -p PORT") {
		t.Errorf("--list did not print the port list:\n%s", out)
	}
}

func TestServeNeedsAPort(t *testing.T) {
	env, _, _ := newTestEnv(t)
	if err := run(t.Context(), []string{"__serve"}, env); !errors.Is(err, errUsage) {
		t.Errorf("run() = %v, want a usage error", err)
	}
}

func TestStateFilePath(t *testing.T) {
	got := stateFilePath("/state", 42)
	if !strings.HasSuffix(got, "42.json") {
		t.Errorf("stateFilePath() = %q, want it to end in 42.json", got)
	}
	if !strings.Contains(got, "runs") {
		t.Errorf("stateFilePath() = %q, want it under the runs directory", got)
	}
}

func TestPrintSharedDescribesTheScope(t *testing.T) {
	public := state.Run{
		Port: 3000,
		Mode: state.ModeFunnel,
		URLs: []string{"http://tailport"},
		Path: "",
	}
	env, out, _ := newTestEnv(t)
	printShared(env, public, false)
	if !contains(out.String(), "the public web") {
		t.Errorf("public run not described as public:\n%s", out)
	}
	if !contains(out.String(), "Ctrl-C") {
		t.Errorf("foreground run does not say how to stop:\n%s", out)
	}

	private := public
	private.Mode = state.ModeTailnet
	env, out, _ = newTestEnv(t)
	printShared(env, private, true)
	if !contains(out.String(), "your tailnet only") {
		t.Errorf("private run not described as tailnet-only:\n%s", out)
	}
	if !contains(out.String(), "tailport stop") {
		t.Errorf("background run does not say how to stop:\n%s", out)
	}
}

func TestDaemonOptionsFillInDefaults(t *testing.T) {
	t.Setenv("TAILPORT_STATE_DIR", t.TempDir())
	t.Setenv("TAILPORT_HOSTNAME", "")
	opts := shareOpts{Port: 3000}.daemonOptions()
	if opts.Hostname != "tailport" {
		t.Errorf("Hostname = %q, want tailport", opts.Hostname)
	}
	if opts.LogPath == "" {
		t.Error("LogPath is empty, want a default log path")
	}
	if !strings.Contains(opts.Args()[0], "__serve") {
		t.Errorf("Args() = %v, want the hidden serve command", opts.Args())
	}
}

func TestCommonFlagsBuildANodeConfig(t *testing.T) {
	flags := commonFlags{Hostname: "h", StateDir: "/s", AuthKey: "k", Ephemeral: true, Verbose: true}
	cfg := flags.nodeConfig(func(string, ...any) {})
	if cfg.Hostname != "h" || cfg.StateDir != "/s" || cfg.AuthKey != "k" || !cfg.Ephemeral || !cfg.Verbose {
		t.Errorf("nodeConfig() = %+v, want the flags carried over", cfg)
	}
}

func TestUsageMentionsThePortsAndFlags(t *testing.T) {
	env, out, _ := newTestEnv(t)
	usage(env.Out)
	text := out.String()
	for _, want := range []string{"443, 8443, 10000", "--all", "--private", "--daemon", "--path", "--authkey"} {
		if !contains(text, want) {
			t.Errorf("usage does not mention %q", want)
		}
	}
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env, _, _ := newTestEnv(t)
	// Nothing here should block: a cancelled context makes waitForFile give up.
	if err := run(ctx, []string{"status", "--state-dir", t.TempDir()}, env); err != nil {
		t.Errorf("run() error: %v", err)
	}
}
