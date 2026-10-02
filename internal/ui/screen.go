package ui

import (
	"strings"
	"unicode"

	"golang.org/x/term"
	"golang.org/x/text/width"
)

// size re-reads the terminal size, so a resize is picked up between frames. It
// reports zeroes when there is no terminal or the size cannot be read, which
// leaves a frame untrimmed rather than empty.
func size(fd int) (width, height int) {
	if fd < 0 {
		return 0, 0
	}
	w, h, err := term.GetSize(fd)
	if err != nil {
		return 0, 0
	}
	return w, h
}

// style says how one piece of a frame line is painted.
type style int

const (
	stylePlain style = iota
	styleBold
	styleDim
	styleCyan
	styleYellow
	styleGreen
	styleRed
)

// segment is one piece of a frame line: the text as measured, and how to paint
// it. Keeping the two apart is what lets a line be trimmed to the terminal
// width before any escape sequence is written.
type segment struct {
	text  string
	style style
}

// screen is a whole repainted screenful of lines. Lines are collected here first
// and only written out once they are all built, so an interrupted redraw can
// never leave half of the old frame and half of the new one behind.
type screen struct {
	// env paints the pieces, so colour follows the same stream rules as the
	// rest of tailport's output.
	env *Env
	// width is the terminal width lines are trimmed to, or 0 when the size is
	// unknown, which leaves the lines alone.
	width int
	// sb holds the painted lines, each ending in an explicit carriage return.
	sb strings.Builder
	// lines is how many lines were written, so the next frame can step back
	// over exactly that many.
	lines int
}

// newScreen starts a screenful of lines for a terminal of the given width.
func newScreen(env *Env, width int) *screen {
	return &screen{env: env, width: width}
}

// write appends one line, trimmed so it cannot wrap.
//
// Raw mode turns off the terminal's newline translation, so a bare "\n" would
// only step down a row and every line would land further right than the one
// before it. Trimming to the width is what lets the next frame move back up by
// an exact number of lines.
func (f *screen) write(segs ...segment) {
	// A width of 0 means the size could not be read, which leaves the lines
	// alone. A width of 1 or 2 is a real terminal, however narrow, and still has
	// to be clipped: a line that wraps costs a row nobody counted, and the next
	// frame then steps back up by the wrong number and lands in the wrong place.
	// Clipping to zero cells is the honest answer there, not an unlimited line.
	limit, clip := 0, f.width > 0
	if clip {
		limit = f.width - 1
	}

	used := 0
	for _, s := range segs {
		text := s.text
		if clip && cells(text) > limit-used {
			// cut returns nothing for a room that has already run out, so a line
			// cannot overrun by running past its limit.
			text = cut(text, limit-used)
		}
		used += cells(text)
		f.sb.WriteString(paint(f.env, s.style, text))
	}
	f.sb.WriteString("\r\n")
	f.lines++
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
	case styleGreen:
		return e.Green(text)
	case styleRed:
		return e.Red(text)
	}
	return text
}

// cells is how many terminal cells s takes up, which is not the same as the
// number of runes in it. A CJK character is drawn two cells wide and a combining
// accent none at all, so measuring a row in runes lets it overrun the terminal
// and wrap, and a frame that wrapped costs a row nobody counted: the next repaint
// steps back up by the wrong amount and the log ends up painted over itself.
func cells(s string) int {
	n := 0
	for _, r := range s {
		n += cellWidth(r)
	}
	return n
}

// cellWidth is how many terminal cells one rune occupies.
func cellWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		// Combining marks ride on the character before them and take no room of
		// their own, so counting them would overcount the row.
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// cut shortens s to at most n terminal cells without splitting one.
func cut(s string, n int) string {
	if n <= 0 {
		return ""
	}
	used := 0
	for i, r := range s {
		w := cellWidth(r)
		// A rune that is already too wide for the room left is left out whole
		// rather than cut in half, which would be a broken character.
		if used+w > n {
			return s[:i]
		}
		used += w
	}
	return s
}
