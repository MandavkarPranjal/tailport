// Package ui renders tailport's terminal output: colours, notices, and the
// numbered menu used to pick a port.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// Env is the destination of everything tailport prints. Colour is enabled per
// stream at construction time so piped output stays clean.
type Env struct {
	Out    io.Writer
	Err    io.Writer
	In     io.Reader
	colour bool
	tty    bool
	reader *bufio.Reader
}

// NewEnv builds an Env from the process streams, disabling colour when
// NO_COLOR is set, TERM is "dumb", or the stream is not a terminal.
func NewEnv(in io.Reader, out, errOut io.Writer) *Env {
	return &Env{
		Out:    out,
		Err:    errOut,
		In:     in,
		colour: colourEnabled(out),
		tty:    IsTerminal(out),
	}
}

// IsTerminal reports whether f is attached to a character device.
func IsTerminal(f any) bool {
	file, ok := f.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Interactive reports whether tailport may prompt: it needs both a readable
// terminal on stdin and somewhere to draw the menu.
func (e *Env) Interactive() bool {
	return e.tty && IsTerminal(e.In)
}

func colourEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	switch strings.ToLower(os.Getenv("TERM")) {
	case "", "dumb":
		return false
	}
	return IsTerminal(w)
}

func (e *Env) paint(code, s string) string {
	if !e.colour {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Bold renders s in bold.
func (e *Env) Bold(s string) string { return e.paint("1", s) }

// Dim renders s dimmed.
func (e *Env) Dim(s string) string { return e.paint("2", s) }

// Green renders s in green.
func (e *Env) Green(s string) string { return e.paint("32", s) }

// Yellow renders s in yellow.
func (e *Env) Yellow(s string) string { return e.paint("33", s) }

// Cyan renders s in cyan.
func (e *Env) Cyan(s string) string { return e.paint("36", s) }

// Title prints a section heading.
func (e *Env) Title(format string, args ...any) {
	fmt.Fprintf(e.Out, "%s\n", e.Bold(fmt.Sprintf(format, args...)))
}

// Line prints an unadorned line to stdout.
func (e *Env) Line(format string, args ...any) {
	fmt.Fprintf(e.Out, format+"\n", args...)
}

// Detail prints an indented, dimmed line to stdout.
func (e *Env) Detail(format string, args ...any) {
	fmt.Fprintf(e.Out, "  %s\n", e.Dim(fmt.Sprintf(format, args...)))
}

// URL prints a shareable URL on its own indented line, underlined.
func (e *Env) URL(s string) {
	fmt.Fprintf(e.Out, "  %s\n", e.paint("4;1", s))
}

// Ok prints a success line to stdout.
func (e *Env) Ok(format string, args ...any) {
	fmt.Fprintf(e.Out, "%s %s\n", e.Green("✓"), fmt.Sprintf(format, args...))
}

// Notice prints an advisory line to stdout.
func (e *Env) Notice(format string, args ...any) {
	fmt.Fprintf(e.Out, "%s %s\n", e.Yellow("!"), fmt.Sprintf(format, args...))
}

// Errorf prints an error line to stderr.
func (e *Env) Errorf(format string, args ...any) {
	fmt.Fprintf(e.Err, "%s %s\n", e.paint("31", "error:"), fmt.Sprintf(format, args...))
}

// Hint prints a dimmed follow-up line to stderr.
func (e *Env) Hint(format string, args ...any) {
	fmt.Fprintf(e.Err, "  %s\n", e.Dim(fmt.Sprintf(format, args...)))
}

// Ask prints a prompt without a trailing newline and reads one line of input.
// It returns io.EOF when the input ends first.
func (e *Env) Ask(prompt string) (string, error) {
	fmt.Fprint(e.Out, prompt)
	if e.reader == nil {
		e.reader = bufio.NewReader(e.In)
	}
	line, err := e.reader.ReadString('\n')
	if err != nil && (line == "" || err != io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Choice is one row of the selection menu.
type Choice struct {
	// Label is the port, e.g. "3000".
	Label string
	// Note is the optional process or address hint, e.g. "node".
	Note string
	// Value is what Choose returns.
	Value string
	// Manual asks the user to type a value instead of picking a row.
	Manual bool
}

// chooseLines is the fallback menu for input that is not a terminal: a numbered
// list plus a line of input. The user may type a row number or type a
// free-form value, which is returned verbatim when it is not a number. An empty
// answer selects def.
func (e *Env) chooseLines(title string, items []Choice, def int) (string, error) {
	e.Title("%s", title)
	for i, item := range items {
		marker := "  "
		if i+1 == def {
			marker = e.Cyan("› ")
		}
		label := e.Bold(fmt.Sprintf("%2d", i+1))
		row := fmt.Sprintf(" %s %s) %s", marker, label, item.Label)
		if item.Note != "" {
			row += "  " + e.Dim(item.Note)
		}
		if item.Manual {
			row += "  " + e.Dim("(type a port)")
		}
		fmt.Fprintln(e.Out, row)
	}
	for {
		answer, err := e.Ask(fmt.Sprintf("\n%s ", e.Dim(fmt.Sprintf("choice [%d]:", def))))
		if err != nil {
			return "", fmt.Errorf("no selection made")
		}
		if answer == "" {
			answer = strconv.Itoa(def)
		}
		n, convErr := strconv.Atoi(answer)
		if convErr != nil {
			return answer, nil
		}
		if n < 1 || n > len(items) {
			e.Notice("pick a number between 1 and %d", len(items))
			continue
		}
		chosen := items[n-1]
		if !chosen.Manual {
			return chosen.Value, nil
		}
		typed, err := e.Ask("  port: ")
		if err != nil {
			return "", fmt.Errorf("no port typed")
		}
		return typed, nil
	}
}
