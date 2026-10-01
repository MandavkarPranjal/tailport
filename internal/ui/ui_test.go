package ui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newEnv builds an Env writing to buffers, with colour forced off.
func newEnv(t *testing.T, in string) (*Env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	t.Setenv("NO_COLOR", "1")
	return NewEnv(strings.NewReader(in), &out, &errOut), &out, &errOut
}

func TestNewEnvDisablesColourWithoutTerminal(t *testing.T) {
	env, _, _ := newEnv(t, "")
	if env.colour {
		t.Error("colour should be off when stdout is a buffer")
	}
	if env.tty {
		t.Error("tty should be false when stdout is a buffer")
	}
	if env.Interactive() {
		t.Error("Interactive should be false without a terminal")
	}
}

func TestNOColorDisablesColourOnTerminal(t *testing.T) {
	var out bytes.Buffer
	t.Setenv("NO_COLOR", "1")
	env := NewEnv(strings.NewReader(""), &out, &out)
	if got := env.Bold("x"); got != "x" {
		t.Errorf("Bold = %q, want plain x under NO_COLOR", got)
	}
}

func TestDumbTerminalDisablesColour(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if colourEnabled(&bytes.Buffer{}) {
		t.Error("TERM=dumb should disable colour")
	}
}

func TestPaintAppliesCodesWhenEnabled(t *testing.T) {
	out := &bytes.Buffer{}
	env := &Env{Out: out, Err: &bytes.Buffer{}, In: strings.NewReader(""), colour: true}
	if got := env.Green("ok"); got != "\x1b[32mok\x1b[0m" {
		t.Errorf("Green = %q", got)
	}
	env.URL("https://x/")
	if got := out.String(); got != "  \x1b[4;1mhttps://x/\x1b[0m\n" {
		t.Errorf("URL output = %q", got)
	}
}

func TestLineAndDetail(t *testing.T) {
	env, out, _ := newEnv(t, "")
	env.Line("plain %d", 1)
	env.Detail("dim %s", "x")
	got := out.String()
	if !strings.Contains(got, "plain 1\n") {
		t.Errorf("Line output = %q", got)
	}
	if !strings.Contains(got, "  dim x\n") {
		t.Errorf("Detail should indent, got %q", got)
	}
}

func TestOkNoticeErrorfHint(t *testing.T) {
	env, out, errOut := newEnv(t, "")
	env.Ok("done")
	env.Notice("careful")
	env.Errorf("bad: %s", "reason")
	env.Hint("try this")

	if !strings.Contains(out.String(), "✓ done") {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(out.String(), "! careful") {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "error: bad: reason") {
		t.Errorf("stderr = %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "try this") {
		t.Errorf("Hint should go to stderr, got %q", errOut.String())
	}
}

func TestAskTrimsAndStripsNewline(t *testing.T) {
	env, _, _ := newEnv(t, "  3000  \n")
	answer, err := env.Ask("port: ")
	if err != nil {
		t.Fatalf("Ask error: %v", err)
	}
	if answer != "3000" {
		t.Errorf("Ask = %q, want 3000", answer)
	}
}

func TestAskReturnsLastLineWithoutNewline(t *testing.T) {
	env, _, _ := newEnv(t, "3000")
	answer, err := env.Ask("port: ")
	if err != nil {
		t.Fatalf("Ask error: %v", err)
	}
	if answer != "3000" {
		t.Errorf("Ask = %q, want 3000", answer)
	}
}

func TestAskErrorsOnEmptyInput(t *testing.T) {
	env, _, _ := newEnv(t, "")
	if _, err := env.Ask("port: "); err == nil {
		t.Error("Ask on empty input should fail")
	}
}

func choices() []Choice {
	return []Choice{
		{Label: "3000", Note: "node (42)", Value: "3000"},
		{Label: "5173", Note: "python3 (43)", Value: "5173"},
		{Label: "other", Note: "type a different port", Manual: true},
	}
}

func TestChooseByNumber(t *testing.T) {
	env, out, _ := newEnv(t, "2\n")
	got, err := env.Choose("Which port?", choices(), 1)
	if err != nil {
		t.Fatalf("Choose error: %v", err)
	}
	if got != "5173" {
		t.Errorf("Choose = %q, want 5173", got)
	}
	if !strings.Contains(out.String(), "3000") || !strings.Contains(out.String(), "node (42)") {
		t.Errorf("menu should list the choices, got %q", out.String())
	}
}

func TestChooseDefaultsOnEmptyAnswer(t *testing.T) {
	env, _, _ := newEnv(t, "\n")
	got, err := env.Choose("Which port?", choices(), 2)
	if err != nil {
		t.Fatalf("Choose error: %v", err)
	}
	if got != "5173" {
		t.Errorf("Choose = %q, want the default row 5173", got)
	}
}

func TestChooseAcceptsTypedPort(t *testing.T) {
	env, _, _ := newEnv(t, "3\n9999\n")
	got, err := env.Choose("Which port?", choices(), 1)
	if err != nil {
		t.Fatalf("Choose error: %v", err)
	}
	if got != "9999" {
		t.Errorf("Choose = %q, want the typed 9999", got)
	}
}

func TestChooseRetriesOutOfRange(t *testing.T) {
	env, out, _ := newEnv(t, "9\n1\n")
	got, err := env.Choose("Which port?", choices(), 1)
	if err != nil {
		t.Fatalf("Choose error: %v", err)
	}
	if got != "3000" {
		t.Errorf("Choose = %q, want 3000 after retry", got)
	}
	if !strings.Contains(out.String(), "between 1 and 3") {
		t.Errorf("out-of-range answer should be reported, got %q", out.String())
	}
}

func TestChooseManualRowPromptsForValue(t *testing.T) {
	env, out, _ := newEnv(t, "\n1234\n")
	got, err := env.Choose("Which port?", choices(), 3)
	if err != nil {
		t.Fatalf("Choose error: %v", err)
	}
	if got != "1234" {
		t.Errorf("Choose = %q, want 1234 typed at the manual row", got)
	}
	if !strings.Contains(out.String(), "port:") {
		t.Errorf("manual row should ask for a port, got %q", out.String())
	}
}

func TestChooseErrorsOnClosedInput(t *testing.T) {
	env, _, _ := newEnv(t, "")
	if _, err := env.Choose("Which port?", choices(), 1); err == nil {
		t.Error("Choose on empty input should fail")
	}
}

func TestIsTerminal(t *testing.T) {
	if IsTerminal(&bytes.Buffer{}) {
		t.Error("a buffer is not a terminal")
	}
	if IsTerminal("not a file") {
		t.Error("a string is not a terminal")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("a regular file is not a terminal")
	}
}
