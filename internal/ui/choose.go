package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

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
		title:   title,
		items:   items,
		cursor:  def - 1,
		drawn:   0,
		waitFor: "choice",
	}
	return m.run()
}

// menu holds the state of the keyboard driven selection.
type menu struct {
	env   *Env
	in    io.Reader
	title string
	items []Choice
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
func (m *menu) render() {
	if m.drawn > 0 {
		fmt.Fprintf(m.env.Out, "\x1b[%dA\x1b[J", m.drawn)
	}

	var frame strings.Builder
	fmt.Fprintf(&frame, "%s\n", m.env.Bold(m.title))
	fmt.Fprintf(&frame, "  %s\n", m.env.Dim("j/k or arrows to move, enter to share, q to quit"))
	for i, item := range m.items {
		marker := "  "
		label := m.env.Dim(fmt.Sprintf("%2d", i+1))
		text := item.Label
		if i == m.cursor {
			marker = m.env.Cyan("> ")
			label = m.env.Bold(fmt.Sprintf("%2d", i+1))
			text = m.env.Cyan(item.Label)
		}
		fmt.Fprintf(&frame, "%s %s) %s", marker, label, text)
		if item.Note != "" {
			fmt.Fprintf(&frame, "  %s", m.env.Dim(item.Note))
		}
		if item.Manual {
			fmt.Fprintf(&frame, "  %s", m.env.Dim("(type a port)"))
		}
		frame.WriteString("\n")
	}
	if m.hint != "" {
		fmt.Fprintf(&frame, "  %s\n", m.env.Yellow(m.hint))
	}
	fmt.Fprintf(&frame, "  %s\n", m.prompt())

	out := frame.String()
	fmt.Fprint(m.env.Out, out)
	m.drawn = strings.Count(out, "\n")
}

// prompt is the input line under the menu.
func (m *menu) prompt() string {
	label := "choice"
	if m.waitFor == "port" {
		label = "port"
	}
	return fmt.Sprintf("%s %s", m.env.Dim(label+":"), strings.TrimSpace(string(m.typed)))
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
