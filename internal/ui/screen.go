package ui

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
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
	limit := 0
	if f.width > 2 {
		limit = f.width - 1
	}

	used := 0
	for _, s := range segs {
		text := s.text
		if limit > 0 && len(text) > limit-used {
			text = cut(text, limit-used)
		}
		used += utf8.RuneCountInString(text)
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
