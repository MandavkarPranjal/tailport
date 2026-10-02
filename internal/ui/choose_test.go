package ui

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
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
		{name: "bare escape", input: "\x1b", want: key{kind: keyEscape}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readKey(bufio.NewReader(strings.NewReader(tt.input)))
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
	if _, err := readKey(bufio.NewReader(strings.NewReader(""))); err == nil {
		t.Error("readKey on empty input = nil error, want an error")
	}
}

func TestReadKeyReadsOneKeyAtATime(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("jj\r"))
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
	return runMenuItems(t, input, def, choices())
}

// runMenuItems is runMenu with a menu of the caller's choosing.
func runMenuItems(t *testing.T, input string, def int, items []Choice) (string, string, error) {
	t.Helper()
	var out bytes.Buffer
	env := &Env{Out: &out, Err: io.Discard, In: strings.NewReader(input)}
	m := &menu{
		env:     env,
		in:      bufio.NewReader(env.In),
		fd:      -1,
		title:   "Which port?",
		items:   items,
		cursor:  def - 1,
		restore: -1,
		waitFor: "choice",
	}
	m.refilter()
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
	if !strings.HasSuffix(out, "choice: 15\r\n") {
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
	for _, want := range []string{"j/k or arrows", "/ to search", "enter to share", "q to quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("menu does not explain %q:\n%s", want, out)
		}
	}
}

func TestMenuSearchFiltersRows(t *testing.T) {
	// "/" then the process name leaves just the row that runs python3, and enter
	// takes it.
	got, out, err := runMenu(t, "/python\r", 1)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if got != "5173" {
		t.Errorf("search for python = %q, want 5173", got)
	}
	if !strings.Contains(out, "search: python") {
		t.Errorf("the prompt does not show the query:\n%s", out)
	}
	if !strings.Contains(out, "type to filter") {
		t.Errorf("the key map does not change while searching:\n%s", out)
	}
}

func TestMenuSearchMatchesThePortTheProcessAndTheNote(t *testing.T) {
	// The port, the process name and the note all count, since the user may be
	// looking for any of them.
	for _, tt := range []struct{ query, want string }{
		{query: "3000", want: "3000"},
		{query: "NODE", want: "3000"},
		{query: "42", want: "3000"},
	} {
		if got, _, err := runMenu(t, "/"+tt.query+"\r", 1); err != nil || got != tt.want {
			t.Errorf("search for %q = %q, %v, want %s", tt.query, got, err, tt.want)
		}
	}
}

func TestMenuSearchMovesWithinTheMatches(t *testing.T) {
	// Two rows are called node, so the arrow keys move between the matches. The
	// letter keys cannot, because while searching they are part of the query.
	if got, _, err := runMenuItems(t, "/node\x1b[B\r", 1, searchChoices()); err != nil || got != "8080" {
		t.Errorf("down arrow inside a search = %q, %v, want 8080", got, err)
	}
}

func TestMenuSearchLettersGoIntoTheQuery(t *testing.T) {
	// j and k are query characters while searching, not navigation, so they end
	// up in the prompt instead of moving the highlight.
	_, out, _ := runMenu(t, "/jk", 1)
	if got := lastFrame(out); !strings.Contains(got, "search: jk") {
		t.Errorf("the query did not swallow the navigation keys:\n%s", got)
	}
}

func TestMenuSearchIgnoresCaseAndSurroundingSpaces(t *testing.T) {
	if got, _, err := runMenuItems(t, "/  node  \r", 1, searchChoices()); err != nil || got != "3000" {
		t.Errorf("search for padded node = %q, %v, want 3000", got, err)
	}
}

func TestMenuSearchBackspaceWidensTheMatches(t *testing.T) {
	// Emptying the query puts every row back, with the highlight on the first.
	got, _, err := runMenuItems(t, "/node\b\b\b\x1b[B\r", 1, searchChoices())
	if err != nil || got != "5173" {
		t.Errorf("backspacing the query = %q, %v, want 5173", got, err)
	}
}

func TestMenuSearchEscapeGoesBack(t *testing.T) {
	// Escape drops the filter, so the digits typed next count as a row again.
	got, out, err := runMenu(t, "/python\x1b1\r", 2)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if got != "3000" {
		t.Errorf("escape then typing 1 = %q, want row 1 (3000)", got)
	}
	if !strings.Contains(lastFrame(out), "choice: 1") {
		t.Errorf("the prompt is still asking for a search:\n%s", lastFrame(out))
	}
}

func TestMenuSearchEscapePutsTheHighlightBack(t *testing.T) {
	got, _, err := runMenu(t, "j/python\x1b\r", 1)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if got != "5173" {
		t.Errorf("escape from a search = %q, want the row it started on, 5173", got)
	}
}

func TestMenuSearchKeepsGoingWhenNothingMatches(t *testing.T) {
	// Enter must not end the menu when there is nothing to take, and the frame
	// has to say why the list is empty.
	_, out, err := runMenu(t, "/zzzz\r", 1)
	if err == nil {
		t.Error("menu accepted an empty result set")
	}
	if !strings.Contains(out, "nothing matches") {
		t.Errorf("an empty result set is not explained:\n%s", out)
	}
	if !strings.Contains(out, "0 of 3") {
		t.Errorf("an empty result set does not report the counts:\n%s", out)
	}
	if !strings.HasSuffix(out, "search: zzzz\r\n") {
		t.Errorf("the search prompt lost the query:\n%q", out)
	}
}

func TestMenuSearchReportsHowManyRowsMatch(t *testing.T) {
	_, out, _ := runMenuItems(t, "/node", 1, searchChoices())
	if !strings.Contains(out, "2 of 4") {
		t.Errorf("the title does not report the match count:\n%s", out)
	}
}

func TestMenuSearchQueryCanReachThePortPrompt(t *testing.T) {
	// The manual row survives a search, so it still asks for a port, and asking
	// for one ends the search.
	got, out, err := runMenu(t, "/type\r5173\r", 1)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if got != "5173" {
		t.Errorf("search then the manual row = %q, want the typed 5173", got)
	}
	if !strings.Contains(out, "port: 5173") {
		t.Errorf("the manual row did not prompt for a port:\n%s", out)
	}
	if strings.Contains(lastFrame(out), "search: ") {
		t.Errorf("the menu is still searching while asking for a port:\n%s", lastFrame(out))
	}
}

func TestMenuSearchNumbersRowsFromOne(t *testing.T) {
	// A filtered list is numbered from one, so its numbers and its row range
	// always agree with each other.
	_, out, _ := runMenu(t, "/type", 1)
	if got := lastFrame(out); !strings.Contains(got, "1)other") {
		t.Errorf("a filtered row is not numbered from one:\n%s", got)
	}
	if got := lastFrame(out); strings.Contains(got, "3000") {
		t.Errorf("a filtered out row is still drawn:\n%s", got)
	}
}

func TestMenuSearchKeepsTheRowRangeWhenFiltering(t *testing.T) {
	// A search that matches more rows than the terminal has lines still has to
	// scroll, and still has to say so.
	items := manyChoices(20)
	var out bytes.Buffer
	env := &Env{Out: &out, Err: io.Discard, In: strings.NewReader("30")}
	m := &menu{env: env, in: bufio.NewReader(env.In), fd: -1, title: "Which port?", items: items,
		height: 10, cursor: 0, restore: -1, waitFor: "choice"}
	m.query = []rune("301")
	m.searching = true
	m.refilter()
	m.render()
	if lines := strings.Count(out.String(), "\n"); lines > 10 {
		t.Errorf("a filtered frame is %d lines tall on a 10 line terminal", lines)
	}
	if !strings.Contains(out.String(), "1-5 of 10") {
		t.Errorf("the frame does not say how many rows there are:\n%s", out.String())
	}
	if strings.Contains(out.String(), "3000") {
		t.Errorf("a row that does not match is still drawn:\n%s", out.String())
	}
}

func TestMenuEscapeOutsideASearchChangesNothing(t *testing.T) {
	// Escape is only meaningful in a search, so it must not move the highlight.
	if got, _, err := runMenu(t, "\x1bj\r", 1); err != nil || got != "5173" {
		t.Errorf("escape then j = %q, %v, want 5173", got, err)
	}
}

// searchChoices is a menu with two rows that share a process name, so a search
// has more than one match to move between.
func searchChoices() []Choice {
	return []Choice{
		{Label: "3000", Note: "node (42)", Value: "3000"},
		{Label: "5173", Note: "python3 (43)", Value: "5173"},
		{Label: "8080", Note: "node (44)", Value: "8080"},
		{Label: "other", Note: "type a different port", Manual: true},
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

// frame returns one drawn frame of a menu with the given terminal width.
func frame(t *testing.T, width int, items []Choice) string {
	t.Helper()
	return draw(t, width, 0, 0, items)
}

// draw renders one frame of a menu at a given terminal size and cursor row.
func draw(t *testing.T, width, height, cursor int, items []Choice) string {
	t.Helper()
	var out bytes.Buffer
	env := &Env{Out: &out, Err: io.Discard, In: strings.NewReader("")}
	m := &menu{
		env: env, in: bufio.NewReader(env.In), fd: -1, title: "Which port?", items: items,
		width: width, height: height, cursor: cursor, restore: -1, waitFor: "choice",
	}
	m.refilter()
	m.render()
	return out.String()
}

// manyChoices is a menu longer than a small terminal.
func manyChoices(n int) []Choice {
	items := make([]Choice, 0, n)
	for i := range n {
		port := fmt.Sprintf("%d", 3000+i)
		items = append(items, Choice{Label: port, Value: port})
	}
	return items
}

func TestFrameFitsTheTerminalHeight(t *testing.T) {
	// A frame taller than the terminal cannot be redrawn in place, so the menu
	// draws a window onto its rows instead.
	items := manyChoices(20)
	for _, cursor := range []int{0, 9, 19} {
		out := draw(t, 0, 10, cursor, items)
		if lines := strings.Count(out, "\n"); lines > 10 {
			t.Errorf("cursor %d drew a %d line frame on a 10 line terminal", cursor, lines)
		}
		if want := fmt.Sprintf("> %2d)%s", cursor+1, items[cursor].Label); !strings.Contains(out, want) {
			t.Errorf("cursor %d is not drawn in the frame, want %q:\n%s", cursor, want, out)
		}
		if !strings.Contains(out, "of 20") {
			t.Errorf("a trimmed frame does not say how many rows there are:\n%s", out)
		}
	}
}

func TestFrameScrollsToKeepTheCursorVisible(t *testing.T) {
	top := draw(t, 0, 10, 0, manyChoices(20))
	if !strings.Contains(top, " 1)") || strings.Contains(top, "20)") {
		t.Errorf("the first frame does not start at row 1:\n%s", top)
	}
	bottom := draw(t, 0, 10, 19, manyChoices(20))
	if !strings.Contains(bottom, "20)") || strings.Contains(bottom, " 1)") {
		t.Errorf("the last frame does not end at row 20:\n%s", bottom)
	}
}

func TestFrameLinesEndWithCarriageReturn(t *testing.T) {
	// Raw mode drops the terminal's newline translation, so a bare "\n" would
	// step down without returning to the first column and every row would drift
	// to the right.
	out := frame(t, 0, choices())
	if !strings.HasPrefix(out, "Which port?\r\n") {
		t.Errorf("frame does not start with the title on its own line:\n%q", out)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Errorf("frame has a line that does not end in \\r\\n:\n%q", out)
	}
}

func TestFrameReturnsToTheFirstColumnOnRedraw(t *testing.T) {
	// ESC [ n A moves up but keeps the column, so the clear needs a carriage
	// return of its own.
	_, out, err := runMenu(t, "j\r", 1)
	if err != nil {
		t.Fatalf("run error: %v", err)
	}
	if !strings.Contains(out, "\x1b[6A\r\x1b[J") {
		t.Errorf("redraw does not return to the first column:\n%q", out)
	}
}

func TestFrameKeepsRowsOnOneLine(t *testing.T) {
	const width = 30
	out := frame(t, width, []Choice{
		{Label: "3000", Note: "node (4242)"},
		{Label: "5173", Note: "a very long process name that will not fit"},
	})
	for _, line := range strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n") {
		if n := utf8.RuneCountInString(line); n > width-1 {
			t.Errorf("line of %d columns overflows a %d column terminal: %q", n, width, line)
		}
	}
	if !strings.Contains(out, "3000") || !strings.Contains(out, "5173") {
		t.Errorf("a short row lost its label:\n%q", out)
	}
}

func TestFrameLinesUpTheNotes(t *testing.T) {
	// Ports are different widths, so the notes only line up if the labels are
	// padded to a common column.
	out := frame(t, 0, []Choice{
		{Label: "53", Note: "127.0.0.1"},
		{Label: "20241", Note: "cloudflared (4242)"},
		{Label: "3000", Note: "node"},
	})
	notes := []string{"127.0.0.1", "cloudflared (4242)", "node"}
	rows := rowsOf(out)
	if len(rows) != len(notes) {
		t.Fatalf("frame drew %d rows, want %d:\n%q", len(rows), len(notes), out)
	}
	want := -1
	for i, row := range rows {
		got := strings.Index(row, notes[i])
		if got < 0 {
			t.Fatalf("row %q is missing the note %q", row, notes[i])
		}
		if want < 0 {
			want = got
		}
		if got != want {
			t.Errorf("note in %q starts at column %d, want %d", row, got, want)
		}
	}
}

// lastFrame is the frame the user is left looking at, which is the one drawn
// after the final redraw.
func lastFrame(out string) string {
	frames := strings.Split(out, "\x1b[J")
	return frames[len(frames)-1]
}

// rowsOf returns the numbered rows of a frame.
func rowsOf(frame string) []string {
	var rows []string
	for _, line := range strings.Split(frame, "\r\n") {
		if strings.Contains(line, ")") {
			rows = append(rows, line)
		}
	}
	return rows
}

func TestFrameAlignsRows(t *testing.T) {
	// The highlighted row starts with "> " instead of two spaces, so its number
	// has to line up with the others one column further right.
	const wantColumn = 4
	for _, row := range rowsOf(frame(t, 0, choices())) {
		if got := strings.Index(row, ")"); got != wantColumn {
			t.Errorf("row %q has its number at column %d, want %d", row, got, wantColumn)
		}
	}
}

func TestCutKeepsWholeRunes(t *testing.T) {
	if got := cut("héllo", 2); got != "hé" {
		t.Errorf("cut = %q, want hé", got)
	}
	if got := cut("héllo", 99); got != "héllo" {
		t.Errorf("cut past the end = %q, want the whole string", got)
	}
	if got := cut("héllo", 0); got != "" {
		t.Errorf("cut to nothing = %q, want an empty string", got)
	}
}

func TestCellsCountsTerminalCellsRatherThanRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{name: "plain ascii is one cell each", in: "abc", want: 3},
		{name: "accented latin is still one cell each", in: "héllo", want: 5},
		// A rune is not a cell: a CJK character is drawn two cells wide, so
		// counting runes here would let a row overrun the terminal and wrap.
		{name: "a wide character takes two cells", in: "日本語", want: 6},
		{name: "wide and narrow mix", in: "a日b", want: 4},
		// A combining accent is drawn on top of the character before it and
		// costs nothing, so counting it would push the row out early.
		{name: "a combining accent takes no cell of its own", in: "é", want: 1},
		{name: "combining marks after a base cost nothing", in: "éx", want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cells(tt.in); got != tt.want {
				t.Errorf("cells(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestCutMeasuresInCellsAndLeavesARuneWhole(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "a wide character does not fit in one cell and is left out whole", in: "日x", n: 1, want: ""},
		{name: "one wide character fills two cells", in: "日x", n: 2, want: "日"},
		{name: "a wide and a narrow one fit in three cells", in: "日x", n: 3, want: "日x"},
		// Cutting mid-rune would leave a broken character on screen.
		{name: "a rune is never cut in half", in: "a日", n: 2, want: "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cut(tt.in, tt.n); got != tt.want {
				t.Errorf("cut(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

func TestScreenDoesNotWrapALineFullOfWideCharacters(t *testing.T) {
	env, _, _ := newEnv(t, "")
	// Wide enough for six cells of path, which is three CJK characters, and too
	// narrow for four.
	s := newScreen(env, 7)

	s.write(segment{"日本語です", stylePlain})

	line := strings.TrimSuffix(s.sb.String(), "\r\n")
	if got := cells(line); got > 6 {
		t.Errorf("line = %q takes %d cells, want at most 6 so it cannot wrap", line, got)
	}
	if s.lines != 1 {
		t.Errorf("lines = %d, want 1: a wrapped line would have cost a row nobody counted", s.lines)
	}
}
