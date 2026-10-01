package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// keyKind is what one key press means to the menu.
type keyKind int

const (
	// keyRune is an ordinary character, carried in the rune with it.
	keyRune keyKind = iota
	// keyUp moves the highlight to the previous row.
	keyUp
	// keyDown moves the highlight to the next row.
	keyDown
	// keyEnter accepts the current selection.
	keyEnter
	// keyQuit abandons the menu.
	keyQuit
)

// key is one decoded key press.
type key struct {
	kind keyKind
	r    rune
}

// Choose draws the selection menu and returns the value the user settled on.
//
// On a real terminal the menu is keyboard driven: j and k and the arrow keys
// move the highlight, enter accepts it, and typing digits gives a port directly.
// When the input is a pipe, a file, or a test buffer, it falls back to a plain
// numbered prompt so scripts and pipes still work.
func (e *Env) Choose(title string, items []Choice, def int) (string, error) {
	if def < 1 {
		def = 1
	}
	if file, ok := e.In.(*os.File); ok && e.tty && term.IsTerminal(int(file.Fd())) {
		if value, err, handled := e.chooseKeys(file, title, items, def); handled {
			return value, err
		}
	}
	return e.chooseLines(title, items, def)
}

// chooseKeys runs the interactive menu. The third return value reports whether
// it could drive the terminal at all; when false the caller falls back.
func (e *Env) chooseKeys(file *os.File, title string, items []Choice, def int) (string, error, bool) {
	fd := int(file.Fd())
	saved, err := term.MakeRaw(fd)
	if err != nil {
		return "", nil, false
	}
	// Raw mode swallows the enter key, so leave the cursor on a clean line.
	defer func() {
		_ = term.Restore(fd, saved)
		fmt.Fprintln(e.Out)
	}()

	m := &menu{
		env:     e,
		in:      file,
		fd:      fd,
		title:   title,
		items:   items,
		cursor:  def - 1,
		drawn:   0,
		waitFor: "choice",
	}
	m.refreshSize()
	return m.run()
}

// menu holds the state of the keyboard driven selection.
type menu struct {
	env *Env
	in  io.Reader
	// fd is the terminal the menu draws on; negative means there is none. stdin
	// is 0, so zero is a real terminal here.
	fd int
	// width and height are the terminal size, used to keep the frame on screen.
	width  int
	height int
	title  string
	items  []Choice
	// cursor is the highlighted row, 0-based.
	cursor int
	// typed is what the user has typed so far, shown as a port.
	typed []rune
	// waitFor is which prompt the menu is showing: "choice" or "port".
	waitFor string
	// drawn is how many lines the last frame took, so it can be overwritten.
	drawn int
	// hint is a one-line complaint about the last keypress, drawn inside the
	// frame so that redrawing stays in sync with the terminal.
	hint string
}

// run draws the menu and handles keys until the user picks or quits.
func (m *menu) run() (string, error, bool) {
	for {
		m.render()
		k, err := readKey(m.in)
		if err != nil {
			return "", fmt.Errorf("no selection made"), true
		}
		switch k.kind {
		case keyQuit:
			return "", fmt.Errorf("no selection made"), true
		case keyUp:
			m.hint = ""
			m.move(-1)
		case keyDown:
			m.hint = ""
			m.move(1)
		case keyEnter:
			value, done, err := m.accept()
			if done || err != nil {
				return value, err, true
			}
		case keyRune:
			m.hint = ""
			switch k.r {
			case 'j':
				m.move(1)
			case 'k':
				m.move(-1)
			case 'g':
				m.cursor = 0
			case 'G':
				m.cursor = len(m.items) - 1
			case 'q':
				return "", fmt.Errorf("no selection made"), true
			case '\b', 0x7f:
				if n := len(m.typed); n > 0 {
					m.typed = m.typed[:n-1]
				}
			default:
				m.typed = append(m.typed, k.r)
			}
		}
	}
}

// move shifts the highlight, wrapping around so j at the bottom lands on top.
func (m *menu) move(delta int) {
	if len(m.items) == 0 {
		return
	}
	m.cursor = (m.cursor + delta + len(m.items)) % len(m.items)
}

// accept resolves the current selection. It reports done when the menu is over,
// and an error only when it cannot continue.
func (m *menu) accept() (value string, done bool, err error) {
	if len(m.items) == 0 {
		return "", false, fmt.Errorf("nothing to choose from")
	}

	typed := strings.TrimSpace(string(m.typed))

	if m.waitFor == "port" {
		if typed == "" {
			return "", false, nil
		}
		return typed, true, nil
	}

	if typed != "" {
		// A number inside the menu picks that row; anything else is a port, so
		// typing 3000 never dead-ends on a menu that happens to be shorter.
		if n, convErr := strconv.Atoi(typed); convErr == nil && n >= 1 && n <= len(m.items) {
			return m.items[n-1].Value, true, nil
		}
		return typed, true, nil
	}

	if m.items[m.cursor].Manual {
		m.waitFor = "port"
		return "", false, nil
	}
	return m.items[m.cursor].Value, true, nil
}

// render draws one frame, overwriting the last one in place.
//
// Every line ends in an explicit carriage return because raw mode turns off the
// terminal's newline translation, so a bare "\n" would only step down a row and
// every line would land further right than the one before it. Rows are cut at
// the terminal width so nothing wraps, which is what lets the next frame move
// back up by an exact number of lines.
func (m *menu) render() {
	m.refreshSize()
	if m.drawn > 0 {
		fmt.Fprintf(m.env.Out, "\x1b[%dA\r\x1b[J", m.drawn)
	}

	var frame strings.Builder
	pad := labelWidth(m.items)
	start, rows := m.window()
	m.writeLine(&frame, m.titleSegments(start, rows)...)
	m.writeLine(&frame, segment{navHint, styleDim})
	for i := start; i < start+rows; i++ {
		item := m.items[i]
		marker := segment{"  ", stylePlain}
		number := segment{fmt.Sprintf("%2d)", i+1), styleDim}
		label := segment{padLabel(item.Label, pad), stylePlain}
		if i == m.cursor {
			marker = segment{"> ", styleCyan}
			number = segment{fmt.Sprintf("%2d)", i+1), styleBold}
			label = segment{padLabel(item.Label, pad), styleCyan}
		}
		segs := []segment{marker, number, label}
		if item.Note != "" {
			segs = append(segs, segment{"  " + item.Note, styleDim})
		}
		if item.Manual {
			segs = append(segs, segment{"  (type a port)", styleDim})
		}
		m.writeLine(&frame, segs...)
	}
	if m.hint != "" {
		m.writeLine(&frame, segment{"  " + m.hint, styleYellow})
	}
	m.writeLine(&frame, m.prompt()...)

	out := frame.String()
	fmt.Fprint(m.env.Out, out)
	m.drawn = strings.Count(out, "\n")
}

// frameChrome is the number of lines the frame spends on the title, the key map,
// an optional hint, and the prompt, plus a spare line. Keeping the spare line
// means a frame is always shorter than the terminal, so drawing it never
// scrolls and the next frame can always move back up by the full line count.
const frameChrome = 5

// minVisibleRows is how many rows are drawn on a terminal too short for the
// whole menu.
const minVisibleRows = 3

// window returns the range of rows to draw, which is a window onto the items
// when there are more of them than the terminal has lines. A frame taller than
// the terminal could not be redrawn in place, so it has to be trimmed.
func (m *menu) window() (start, rows int) {
	rows = len(m.items)
	if m.height > 0 {
		room := m.height - frameChrome
		if room < minVisibleRows {
			room = minVisibleRows
		}
		if room < rows {
			rows = room
		}
	}
	start = 0
	if m.cursor >= start+rows {
		start = m.cursor - rows + 1
	}
	return start, rows
}

// titleSegments is the title, followed by the visible row range when the menu
// is taller than the terminal. It goes on the title line because that line is
// short enough to survive a narrow terminal, where the key map would be cut.
func (m *menu) titleSegments(start, rows int) []segment {
	segs := []segment{{m.title, styleBold}}
	if rows < len(m.items) {
		segs = append(segs, segment{fmt.Sprintf("   %d-%d of %d", start+1, start+rows, len(m.items)), styleDim})
	}
	return segs
}

// navHint is the one-line key map shown under the title.
const navHint = "j/k or arrows to move, enter to share, q to quit"

// style says how one piece of a frame line is painted.
type style int

const (
	stylePlain style = iota
	styleBold
	styleDim
	styleCyan
	styleYellow
)

// segment is one piece of a frame line: the text as measured, and how to paint
// it. Keeping the two apart is what lets a line be trimmed to the terminal
// width before any escape sequence is written.
type segment struct {
	text  string
	style style
}

// writeLine appends one frame line, trimmed so it cannot wrap.
func (m *menu) writeLine(sb *strings.Builder, segs ...segment) {
	limit := 0
	if m.width > 2 {
		limit = m.width - 1
	}

	used := 0
	for _, s := range segs {
		text := s.text
		if limit > 0 && len(text) > limit-used {
			text = cut(text, limit-used)
		}
		used += utf8.RuneCountInString(text)
		sb.WriteString(paint(m.env, s.style, text))
	}
	sb.WriteString("\r\n")
}

// refreshSize re-reads the terminal size, so a resize mid-menu is picked up.
func (m *menu) refreshSize() {
	if m.fd < 0 {
		return
	}
	if w, h, err := term.GetSize(m.fd); err == nil {
		if w > 0 {
			m.width = w
		}
		if h > 0 {
			m.height = h
		}
	}
}

// paint wraps text in the escape codes for s.
func paint(e *Env, s style, text string) string {
	switch s {
	case styleBold:
		return e.Bold(text)
	case styleDim:
		return e.Dim(text)
	case styleCyan:
		return e.Cyan(text)
	case styleYellow:
		return e.Yellow(text)
	}
	return text
}

// cut shortens s to at most n runes without splitting one.
func cut(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

// prompt is the input line under the menu.
func (m *menu) prompt() []segment {
	label := "choice"
	if m.waitFor == "port" {
		label = "port"
	}
	return []segment{
		{"  ", stylePlain},
		{label + ": ", styleDim},
		{string(m.typed), stylePlain},
	}
}

// readKey decodes one key press from raw terminal input. Arrow keys arrive as
// an escape sequence, ESC [ A for up and ESC [ B for down, which is also how
// many terminals spell the home-row variants.
func readKey(r io.Reader) (key, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return key{}, err
	}
	switch b[0] {
	case 0x03, 0x04: // ctrl-c, ctrl-d
		return key{kind: keyQuit}, nil
	case '\r', '\n':
		return key{kind: keyEnter}, nil
	case 0x1b:
		seq, err := readSeq(r)
		if err != nil {
			// A lone escape is not a key we act on, but running out of input
			// while looking for the rest of a sequence is not a failure either.
			return key{kind: keyRune}, nil
		}
		switch seq {
		case "[A", "OA", "[1;2A", "[1;5A":
			return key{kind: keyUp}, nil
		case "[B", "OB", "[1;2B", "[1;5B":
			return key{kind: keyDown}, nil
		}
		return key{kind: keyRune}, nil
	}
	if b[0] < 0x20 {
		return key{kind: keyRune, r: rune(b[0])}, nil
	}
	return key{kind: keyRune, r: rune(b[0])}, nil
}

// readSeq collects the rest of a CSI or SS3 escape sequence. It stops at the
// final byte, which for CSI is anything in 0x40 to 0x7e, so modified arrows
// such as ESC [ 1 ; 2 A are read whole.
func readSeq(r io.Reader) (string, error) {
	var seq strings.Builder
	var b [1]byte

	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	seq.WriteByte(b[0])
	ss3 := b[0] == 'O'
	if b[0] != '[' && !ss3 {
		return "", errNoSequence
	}

	for range 8 {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return "", err
		}
		seq.WriteByte(b[0])
		if b[0] >= 0x40 && b[0] <= 0x7e {
			return seq.String(), nil
		}
	}
	return seq.String(), nil
}

// errNoSequence marks an escape byte that did not introduce a sequence.
var errNoSequence = errors.New("not a sequence")
