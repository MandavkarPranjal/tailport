package ui

import (
	"fmt"
	"strings"
	"testing"
)

func TestScreenLeavesLinesAloneWhenTheTerminalSizeIsUnknown(t *testing.T) {
	env, _, _ := newEnv(t, "")
	s := newScreen(env, 0)

	s.write(segment{"a line far longer than any real terminal", stylePlain})

	// Zero is what size reports when it cannot read the size, and it means
	// nothing about how wide the terminal is, so there is nothing to trim to.
	if !strings.Contains(s.sb.String(), "a line far longer than any real terminal") {
		t.Errorf("line = %q, want it left whole when the width is unknown", s.sb.String())
	}
}

func TestScreenClipsLinesOnEvenTheNarrowestTerminal(t *testing.T) {
	// A line that wraps costs a row nobody counted, so the next frame steps back
	// up by the wrong amount and lands somewhere else entirely.
	for _, width := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("%d cells wide", width), func(t *testing.T) {
			env, _, _ := newEnv(t, "")
			s := newScreen(env, width)

			s.write(segment{"a line far longer than this terminal", stylePlain})

			line := strings.TrimSuffix(s.sb.String(), "\r\n")
			if n := len([]rune(line)); n > width-1 {
				t.Errorf("line = %q (%d cells), want at most %d so it cannot wrap", line, n, width-1)
			}
		})
	}
}

func TestScreenCountsEveryLineEvenWhenOneCannotFitAtAll(t *testing.T) {
	env, _, _ := newEnv(t, "")
	s := newScreen(env, 1)

	for range 3 {
		s.write(segment{"nothing fits here", stylePlain})
	}

	// Clipping to nothing still writes a line, because a frame that claims fewer
	// lines than it painted leaves the top of it on screen for ever.
	if s.lines != 3 {
		t.Errorf("lines = %d, want 3", s.lines)
	}
	if got := s.sb.String(); got != "\r\n\r\n\r\n" {
		t.Errorf("screen = %q, want three empty lines", got)
	}
}

func TestScreenClipsAcrossSegmentsWithoutOverrunningTheWidth(t *testing.T) {
	env, _, _ := newEnv(t, "")
	s := newScreen(env, 8)

	s.write(
		segment{"  ", stylePlain},
		segment{"method", stylePlain},
		segment{"/a/very/long/path", stylePlain},
	)

	// Segments are filled in order and each one is cut to whatever room is left,
	// so the later ones are what gets dropped once the width is spent.
	line := strings.TrimSuffix(s.sb.String(), "\r\n")
	if n := len([]rune(line)); n != 7 {
		t.Errorf("line = %q (%d cells), want exactly the 7 that fit", line, n)
	}
	if strings.Contains(line, "/a/very") {
		t.Errorf("line = %q, want the path dropped once the width was spent", line)
	}
}

func TestSizeReportsZeroesForAFileDescriptorThatIsNotATerminal(t *testing.T) {
	// A caller that gets zeroes back must be able to tell them from a real size,
	// which is what keeps an unknown width from being read as a narrow one.
	if w, h := size(-1); w != 0 || h != 0 {
		t.Errorf("size(-1) = %d, %d, want zeroes", w, h)
	}
}

func TestScreenKeepsWideCharacterColumnsAligned(t *testing.T) {
	// A wide character takes two cells, so a row holding one is a cell narrower
	// than it looks. Padding it as if it were one rune leaves a gap that pushes
	// every column after it out of line.
	if got := padLeft("日", 3); cells(got) != 3 {
		t.Errorf("padLeft(日, 3) = %q takes %d cells, want 3", got, cells(got))
	}
	if got := padRight("日", 3); cells(got) != 3 {
		t.Errorf("padRight(日, 3) = %q takes %d cells, want 3", got, cells(got))
	}
}
