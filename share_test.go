package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/node"
	"github.com/MandavkarPranjal/tailport/internal/state"
)

// fakeStarter returns a node.Starter that hands out a Fake node, so a test can
// share a port without a tailnet. It also restores the real starter afterwards.
func fakeStarter(t *testing.T) (*node.Fake, node.Starter) {
	t.Helper()
	fake := node.NewFake(node.FakeConfig{})
	start := func(context.Context, node.Config, func(string)) (node.Node, error) {
		return fake, nil
	}
	t.Cleanup(func() { _ = fake.Close() })
	return fake, start
}

// waitForState blocks until the run writes its state file.
func waitForState(t *testing.T, stateDir string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if runs, err := state.List(stateDir); err == nil && len(runs) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the run never recorded its state")
}

// upstream returns a local test server and its port, standing in for the
// service the user is sharing.
func upstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Path", r.URL.Path)
		w.Header().Set("X-Seen-Host", r.Host)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// get fetches a URL and returns the status and body.
func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s: %v", url, err)
	}
	return resp.StatusCode, string(data)
}

// getHeader reads one response header the test upstream echoes back.
func getHeader(t *testing.T, url, name string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header.Get(name)
}

func TestServeSharesTheLocalPortOverTheTailnetListener(t *testing.T) {
	svc := upstream(t, "hello from the app")
	_, start := fakeStarter(t)

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	env, out, _ := newTestEnv(t)
	go func() {
		done <- serveWith(ctx, env, shareOpts{
			commonFlags: commonFlags{StateDir: dir},
			Port:        portOf(t, svc.URL),
			PublicPort:  node.DefaultPublicPort,
		}, os.Getpid(), "", start)
	}()
	waitForState(t, dir)
	run, err := state.Load(dir, os.Getpid())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	status, body := get(t, run.URLs[0])
	if status != http.StatusOK || body != "hello from the app" {
		t.Errorf("GET %s = %d %q, want 200 %q", run.URLs[0], status, body, "hello from the app")
	}
	if seen := getHeader(t, run.URLs[0], "X-Seen-Host"); seen == "" {
		t.Errorf("the service saw no Host header, so a dev server that checks it would refuse the request")
	}
	if run.Mode != state.ModeFunnel {
		t.Errorf("Mode = %q, want %q", run.Mode, state.ModeFunnel)
	}
	if len(run.URLs) != 2 {
		t.Errorf("URLs = %v, want a tailnet and a public address", run.URLs)
	}
	if !contains(out.String(), run.URLs[0]) {
		t.Errorf("printed output %q does not mention %q", out.String(), run.URLs[0])
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveWith error: %v", err)
	}
}

func TestServeUnderAPathPrefix(t *testing.T) {
	svc := upstream(t, "mounted")
	fake, start := fakeStarter(t)

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	env, _, _ := newTestEnv(t)
	go func() {
		done <- serveWith(ctx, env, shareOpts{
			commonFlags: commonFlags{StateDir: dir},
			Port:        portOf(t, svc.URL),
			Path:        "/demo",
			PublicPort:  node.DefaultPublicPort,
		}, os.Getpid(), "", start)
	}()
	waitForState(t, dir)
	run, err := state.Load(dir, os.Getpid())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	base := strings.TrimSuffix(fake.TailnetURL(), "/")
	if got, want := run.Path, "/demo"; got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}

	status, body := get(t, base+"/demo/")
	if status != http.StatusOK || body != "mounted" {
		t.Errorf("GET %s/demo/ = %d %q, want 200 %q", base, status, body, "mounted")
	}
	// The bare prefix has to reach the service's root, or a dev server 404s.
	if status, body := get(t, base+"/demo"); status != http.StatusOK || body != "mounted" {
		t.Errorf("GET %s/demo = %d %q, want 200 %q", base, status, body, "mounted")
	}
	// Anything outside the prefix points at the right URL.
	if status, _ := get(t, base+"/nope"); status != http.StatusNotFound {
		t.Errorf("GET %s/nope = %d, want 404", base, status)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveWith error: %v", err)
	}
}

func TestServePrivateSkipsThePublicListener(t *testing.T) {
	svc := upstream(t, "private")
	fake, start := fakeStarter(t)

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	env, _, _ := newTestEnv(t)
	go func() {
		done <- serveWith(ctx, env, shareOpts{
			commonFlags: commonFlags{StateDir: dir},
			Port:        portOf(t, svc.URL),
			Private:     true,
			PublicPort:  node.DefaultPublicPort,
		}, os.Getpid(), "", start)
	}()
	waitForState(t, dir)
	run, err := state.Load(dir, os.Getpid())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	if run.Mode != state.ModeTailnet {
		t.Errorf("Mode = %q, want %q", run.Mode, state.ModeTailnet)
	}
	if len(run.URLs) != 1 {
		t.Errorf("URLs = %v, want only the tailnet address", run.URLs)
	}
	if got := fake.PublicURL(node.DefaultPublicPort); got != "" {
		t.Errorf("a private run published to %q, want nothing public", got)
	}
	if status, body := get(t, run.URLs[0]); status != http.StatusOK || body != "private" {
		t.Errorf("GET %s = %d %q, want 200 %q", run.URLs[0], status, body, "private")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveWith error: %v", err)
	}
}

func TestServePublishesEveryPublicPortWithAll(t *testing.T) {
	svc := upstream(t, "all ports")
	fake, start := fakeStarter(t)

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	env, _, _ := newTestEnv(t)
	go func() {
		done <- serveWith(ctx, env, shareOpts{
			commonFlags: commonFlags{StateDir: dir},
			Port:        portOf(t, svc.URL),
			All:         true,
			Private:     false,
			PublicPort:  node.DefaultPublicPort,
		}, os.Getpid(), "", start)
	}()
	waitForState(t, dir)
	run, err := state.Load(dir, os.Getpid())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	if want := len(node.PublicPorts) + 1; len(run.URLs) != want {
		t.Fatalf("URLs = %v, want %d addresses", run.URLs, want)
	}
	for _, public := range node.PublicPorts {
		url := fake.PublicURL(public)
		if url == "" {
			t.Errorf("public port %d was not published", public)
			continue
		}
		if status, body := get(t, url); status != http.StatusOK || body != "all ports" {
			t.Errorf("GET %s = %d %q, want 200 %q", url, status, body, "all ports")
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveWith error: %v", err)
	}
}

func TestServeReportsAnUnreachableLocalPort(t *testing.T) {
	_, start := fakeStarter(t)

	// A port nothing listens on: the proxy has to say so instead of hanging.
	closed := upstream(t, "gone")
	port := portOf(t, closed.URL)
	closed.Close()

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	env, _, _ := newTestEnv(t)
	go func() {
		done <- serveWith(ctx, env, shareOpts{
			commonFlags: commonFlags{StateDir: dir},
			Port:        port,
			PublicPort:  node.DefaultPublicPort,
		}, os.Getpid(), "", start)
	}()
	waitForState(t, dir)
	run, err := state.Load(dir, os.Getpid())
	if err != nil {
		t.Fatalf("load state: %v", err)
	}

	if status, _ := get(t, run.URLs[0]); status != http.StatusBadGateway {
		t.Errorf("GET a dead port = %d, want 502", status)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serveWith error: %v", err)
	}
}

// portOf reads the port out of a URL like http://127.0.0.1:34567.
func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	idx := strings.LastIndex(rawURL, ":")
	if idx < 0 {
		t.Fatalf("no port in %q", rawURL)
	}
	port, err := strconv.Atoi(rawURL[idx+1:])
	if err != nil {
		t.Fatalf("port in %q: %v", rawURL, err)
	}
	return port
}
