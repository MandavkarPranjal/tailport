package ui

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestReadKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  key
	}{
		{name: "down arrow", input: "\x1b[B", want: key{kind: keyDown}},
		{name: "up arrow", input: "\x1b[A", want: key{kind: keyUp}},
		{name: "arrow application mode", input: "\x1bOB", want: key{kind: keyDown}},
		{name: "shift arrow", input: "\x1b[1;2A", want: key{kind: keyUp}},
		{name: "enter", input: "\r", want: key{kind: keyEnter}},
		{name: "newline", input: "\n", want: key{kind: keyEnter}},
		{name: "ctrl-c quits", input: "\x03", want: key{kind: keyQuit}},
		{name: "ctrl-d quits", input: "\x04", want: key{kind: keyQuit}},
		{name: "letter", input: "j", want: key{kind: keyRune, r: 'j'}},
		{name: "digit", input: "3", want: key{kind: keyRune, r: '3'}},
		{name: "space", input: " ", want: key{kind: keyRune, r: ' '}},
		{name: "backspace", input: "\x7f", want: key{kind: keyRune, r: 0x7f}},
		{name: "bare escape is ignored", input: "\x1b", want: key{kind: keyRune}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readKey(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("readKey(%q) error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("readKey(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestReadKeyOnClosedInput(t *testing.T) {
	if _, err := readKey(strings.NewReader("")); err == nil {
		t.Error("readKey on empty input = nil error, want an error")
	}
}

func TestReadKeyReadsOneKeyAtATime(t *testing.T) {
	r := strings.NewReader("jj\r")
	first, err := readKey(r)
	if err != nil || first.r != 'j' {
		t.Fatalf("first readKey = %+v, %v, want j", first, err)
	}
	second, err := readKey(r)
	if err != nil || second.r != 'j' {
		t.Fatalf("second readKey = %+v, %v, want j", second, err)
	}
	third, err := readKey(r)
	if err != nil || third.kind != keyEnter {
		t.Fatalf("third readKey = %+v, %v, want enter", third, err)
	}
}

// runMenu drives the interactive menu from a scripted input without needing a
// terminal, which is what menu.run does once the terminal is in raw mode.
func runMenu(t *testing.T, input string, def int) (string, string, error) {
	t.Helper()
	var out bytes.Buffer
	env := &Env{Out: &out, Err: io.Discard, In: strings.NewReader(input)}
	m := &menu{
		env:     env,
		in:      env.In,
		title:   "Which port?",
		items:   choices(),
		cursor:  def - 1,
		waitFor: "choice",
	}
	value, err, _ := m.run()
	return value, out.String(), err
}

func TestMenuMovesWithJK(t *testing.T) {
	if got, _, _ := runMenu(t, "j\r", 1); got != "5173" {
		t.Errorf("j from row 1 = %q, want 5173", got)
	}
	if got, _, _ := runMenu(t, "k\r", 2); got != "3000" {
		t.Errorf("k from row 2 = %q, want 3000", got)
	}
}

func TestMenuMovesWithArrows(t *testing.T) {
	if got, _, _ := runMenu(t, "\x1b[B\r", 1); got != "5173" {
		t.Errorf("down arrow = %q, want 5173", got)
	}
	if got, _, _ := runMenu(t, "\x1b[A\r", 2); got != "3000" {
		t.Errorf("up arrow = %q, want 3000", got)
	}
}

func TestMenuWrapsAtBothEnds(t *testing.T) {
	// Row 3 is the manual row, so wrapping onto it asks for a port instead of
	// returning a value.
	got, out, err := runMenu(t, "\x1b[A\r4321\r", 1)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if got != "4321" || !strings.Contains(out, "port:") {
		t.Errorf("up from the first row = %q (port prompt %v), want the manual row", got, strings.Contains(out, "port:"))
	}
	if got, _, err := runMenu(t, "j\r", 3); err != nil || got != "3000" {
		t.Errorf("down past the last row = %q, %v, want 3000", got, err)
	}
}

func TestMenuGoToTopAndBottom(t *testing.T) {
	if got, _, err := runMenu(t, "G\r8080\r", 1); err != nil || got != "8080" {
		t.Errorf("G = %q, %v, want the manual row on the last line", got, err)
	}
	if got, _, _ := runMenu(t, "g\r", 3); got != "3000" {
		t.Errorf("g = %q, want the first row", got)
	}
}

func TestMenuAcceptsTheHighlightedRowOnEnter(t *testing.T) {
	if got, _, _ := runMenu(t, "\r", 2); got != "5173" {
		t.Errorf("enter = %q, want the default row 5173", got)
	}
}

func TestMenuTreatsDigitsInsideTheMenuAsARow(t *testing.T) {
	if got, _, _ := runMenu(t, "1\r", 3); got != "3000" {
		t.Errorf("typing 1 = %q, want row 1 (3000)", got)
	}
}

func TestMenuTreatsOtherDigitsAsAPort(t *testing.T) {
	if got, _, _ := runMenu(t, "1234\r", 1); got != "1234" {
		t.Errorf("typing 1234 = %q, want it used as a port", got)
	}
	if got, _, _ := runMenu(t, "3000\r", 1); got != "3000" {
		t.Errorf("typing 3000 = %q, want it used as a port, not a row", got)
	}
}

func TestMenuBackspace(t *testing.T) {
	got, out, err := runMenu(t, "12\x7f5\r", 1)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if got != "15" {
		t.Errorf("backspace = %q, want 15", got)
	}
	if !strings.HasSuffix(out, "choice: 15\n") {
		t.Errorf("the prompt did not show the corrected value:\n%s", out)
	}
}

func TestMenuManualRowAsksForAPort(t *testing.T) {
	got, out, err := runMenu(t, "jj\r5173\r", 1)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if got != "5173" {
		t.Errorf("manual row = %q, want the typed 5173", got)
	}
	if !strings.Contains(out, "port: 5173") {
		t.Errorf("manual row did not prompt for a port:\n%s", out)
	}
}

func TestMenuQuits(t *testing.T) {
	for _, quit := range []string{"q", "\x03", "\x04"} {
		if _, _, err := runMenu(t, quit, 1); err == nil {
			t.Errorf("%q = nil error, want the menu to be abandoned", quit)
		}
	}
}

func TestMenuStopsOnClosedInput(t *testing.T) {
	if _, _, err := runMenu(t, "", 1); err == nil {
		t.Error("closed input = nil error, want an error")
	}
}

func TestMenuRedrawsInPlace(t *testing.T) {
	_, out, err := runMenu(t, "j\r", 1)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if clears := strings.Count(out, "\x1b[J"); clears != 1 {
		t.Errorf("menu cleared the screen %d times, want once per redraw after the first frame", clears)
	}
	if !strings.Contains(out, "\x1b[J") {
		t.Error("redraw does not clear the previous frame")
	}
	if strings.Count(out, "Which port?") != 2 {
		t.Errorf("title printed %d times, want one per frame", strings.Count(out, "Which port?"))
	}
}

func TestMenuNeverComplainsAboutAPort(t *testing.T) {
	// A typed port is always accepted, so the menu has no out-of-range
	// complaint to draw and the frame keeps its shape.
	_, out, err := runMenu(t, "1234\r", 1)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if strings.Contains(out, "pick a number") {
		t.Errorf("menu complained about a valid port:\n%s", out)
	}
}

func TestMenuShowsHowToNavigate(t *testing.T) {
	_, out, _ := runMenu(t, "\r", 1)
	for _, want := range []string{"j/k or arrows", "enter to share", "q to quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("menu does not explain %q:\n%s", want, out)
		}
	}
}

func TestChooseFallsBackWhenInputIsNotATerminal(t *testing.T) {
	// A strings.Reader is never a terminal, so this must take the line-based
	// path even though the menu code is available.
	env, out, _ := newEnv(t, "2\n")
	got, err := env.Choose("Which port?", choices(), 1)
	if err != nil {
		t.Fatalf("Choose error: %v", err)
	}
	if got != "5173" {
		t.Errorf("Choose = %q, want 5173", got)
	}
	if strings.Contains(out.String(), "j/k or arrows") {
		t.Error("the keyboard menu was drawn for non-terminal input")
	}
}
