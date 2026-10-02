package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MandavkarPranjal/tailport/internal/reqlog"
	"golang.org/x/term"
)

// redrawInterval is how long the dashboard waits before painting again when
// nothing happened. The only thing that can change without a request or a key
// is the terminal size, so this is a resize poll and nothing more.
const redrawInterval = time.Second

// maxKept is how many requests the dashboard holds on to. A share left running
// all afternoon must not grow the watcher's memory without bound, and no
// terminal shows more than a few dozen rows at a time.
const maxKept = 1000

// Dashboard shows what a share is serving, live, and returns when ctx ends or
// the user stops it.
//
// On a terminal the log repaints in place: one row per request, newest at the
// bottom, with a bar whose width is that request's latency measured against the
// slowest request on screen, tinted by status class. When the output is a pipe
// or a file there is nothing to repaint onto, so it prints one line per request
// instead and `tailport watch | grep 502` keeps working.
//
// history is what the log already held, drawn before anything new arrives so the
// view is not empty on a run that has been going for a while. events is the live
// end of the same log. dropped may be nil, and is asked how many requests the
// log could not hand over because the dashboard fell behind.
func (e *Env) Dashboard(ctx context.Context, title string, history []reqlog.Event, events <-chan reqlog.Event, dropped func() int) error {
	d := &dashboard{env: e, title: title, dropped: dropped}
	d.push(history)

	// Raw mode is only worth entering when there is both a terminal to draw on
	// and keys to read from it. Anything else falls back to the line stream,
	// which still works when the user can only interrupt with ctrl-c.
	if e.Interactive() {
		file := e.In.(*os.File)
		fd := int(file.Fd())
		saved, err := term.MakeRaw(fd)
		if err == nil {
			d.fd = fd
			d.in = bufio.NewReader(file)
			d.refreshSize()
			// Raw mode swallows the enter key, so leave the cursor on a clean
			// line and restore the terminal however this ends.
			defer func() {
				_ = term.Restore(fd, saved)
				fmt.Fprintln(e.Out)
			}()
		}
	}
	if d.in == nil {
		return e.streamRequests(ctx, history, events)
	}
	return d.run(ctx, events)
}

// dashboard holds the state of the live request log.
type dashboard struct {
	env   *Env
	title string
	// in decodes key presses, and is nil when there is no terminal to read them
	// from, which is how the dashboard tells it has to fall back to lines.
	in keyReader
	// fd is the terminal the dashboard draws on. stdin is 0, so zero is a real
	// terminal here.
	fd int
	// width and height are the terminal size, used to keep the frame on screen.
	width  int
	height int
	// events is the rolling log, oldest first, capped at maxKept.
	events []reqlog.Event
	// drawn is how many lines the last frame took, so it can be overwritten.
	drawn int
	// dropped reports how many requests the log could not deliver, if it knows.
	dropped func() int
}

// run repaints until the context ends, the log ends, or the user quits.
func (d *dashboard) run(ctx context.Context, events <-chan reqlog.Event) error {
	keys := make(chan key, 8)
	go d.readKeys(ctx, keys)

	tick := time.NewTicker(redrawInterval)
	defer tick.Stop()

	for {
		d.render()
		select {
		case <-ctx.Done():
			return nil
		case k, ok := <-keys:
			if !ok {
				// The terminal stopped handing over keys, which happens when it
				// goes away entirely. Keep watching anyway; ctrl-c still ends it.
				keys = nil
				break
			}
			if d.key(k) {
				return nil
			}
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			d.push([]reqlog.Event{ev})
			// Take whatever else has already arrived too, so a burst of requests
			// costs one repaint instead of one per request.
			d.absorb(events)
		case <-tick.C:
		}
	}
}

// readKeys decodes key presses until the terminal stops or the context ends.
//
// It runs in its own goroutine because the dashboard has to keep repainting
// while nobody is pressing anything, and a blocking read on a terminal cannot
// be interrupted from another goroutine. Whatever read is in flight when the
// dashboard closes finishes into the channel nobody is reading any more, so it
// costs at most one keystroke once the terminal is back with the shell.
func (d *dashboard) readKeys(ctx context.Context, out chan<- key) {
	for {
		k, err := readKey(d.in)
		if err != nil {
			close(out)
			return
		}
		select {
		case out <- k:
		case <-ctx.Done():
			return
		}
	}
}

// key handles one key press and reports whether the dashboard should close.
func (d *dashboard) key(k key) bool {
	switch k.kind {
	case keyQuit:
		return true
	case keyRune:
		if k.r == 'c' || k.r == 'C' {
			d.clear()
		}
	}
	return false
}

// clear throws away the log but keeps watching, which is what somebody wants
// when they are looking for the next thing to go wrong rather than the last one.
func (d *dashboard) clear() {
	d.events = d.events[:0]
}

// absorb takes every request already sitting in the log without waiting.
func (d *dashboard) absorb(events <-chan reqlog.Event) {
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			d.push([]reqlog.Event{ev})
		default:
			return
		}
	}
}

// push adds events to the log, forgetting the oldest ones once it is full.
//
// The copy only happens once the log is full, and at maxKept rows it is a single
// memmove. A ring buffer would avoid it but would make every read a modulo, and
// the dashboard only ever looks at the tail.
func (d *dashboard) push(events []reqlog.Event) {
	for _, ev := range events {
		if len(d.events) == maxKept {
			copy(d.events, d.events[1:])
			d.events[maxKept-1] = ev
			continue
		}
		d.events = append(d.events, ev)
	}
}

// dashChrome is the header, the footer and a spare line. Keeping the spare line
// means a frame is always shorter than the terminal, so painting it never
// scrolls and the next frame can always step back over the full line count.
const dashChrome = 3

// window returns how many request rows fit on screen: the newest ones, since
// this is a live log rather than a page of history. It is everything on offer
// when the terminal size is unknown.
func (d *dashboard) window() int {
	if d.height <= 0 {
		return len(d.events)
	}
	room := d.height - dashChrome
	if room < 1 {
		room = 1
	}
	return min(room, len(d.events))
}

// refreshSize re-reads the terminal size, so a resize is picked up mid-log.
func (d *dashboard) refreshSize() {
	width, height := size(d.fd)
	if width > 0 {
		d.width = width
	}
	if height > 0 {
		d.height = height
	}
}

// render paints one frame, overwriting the last one in place.
func (d *dashboard) render() {
	d.refreshSize()
	if d.drawn > 0 {
		fmt.Fprintf(d.env.Out, "\x1b[%dA\r\x1b[J", d.drawn)
	}

	rows := d.window()
	shown := d.events[len(d.events)-rows:]
	s := newScreen(d.env, d.width)
	s.write(d.header()...)
	layout := layoutRow(d.width, longestPath(shown))
	for _, ev := range shown {
		s.write(requestRow(ev, layout, slowest(d.events))...)
	}
	if len(d.events) == 0 {
		s.write(segment{"  waiting for requests...", styleDim})
	}
	s.write(d.footer()...)

	fmt.Fprint(d.env.Out, s.sb.String())
	d.drawn = s.lines
}

// header is the title, followed by what the log has added up to: how many
// requests, how slow the slowest one was, and how many of them went wrong. The
// failures are the only number worth tinting, since they are the reason a person
// is watching at all.
func (d *dashboard) header() []segment {
	segs := []segment{{d.title, styleBold}, {"   ", stylePlain}}
	if len(d.events) == 0 {
		return append(segs, segment{"live", styleDim})
	}

	segs = append(segs, segment{plural(len(d.events), "request"), styleDim})
	if peak := slowest(d.events); peak > 0 {
		segs = append(segs, segment{"   slowest " + latency(peak), styleDim})
	}
	if failed := failures(d.events); failed > 0 {
		segs = append(segs, segment{"   ", stylePlain}, segment{plural(failed, "failure"), styleRed})
	}
	if missed := d.missed(); missed > 0 {
		// Say so rather than quietly showing fewer requests than were served:
		// a log that lies about its own counts is worse than no log.
		segs = append(segs, segment{"   ", stylePlain}, segment{plural(missed, "request") + " not shown", styleYellow})
	}
	return segs
}

// footer is the one-line key map.
func (d *dashboard) footer() []segment {
	hint := "c to clear, q or ctrl-c to stop"
	if len(d.events) == 0 {
		hint = "waiting for the share to answer something, q or ctrl-c to stop"
	}
	return []segment{{hint, styleDim}}
}

// missed asks the log how many requests it had to drop, if it tracks that.
func (d *dashboard) missed() int {
	if d.dropped == nil {
		return 0
	}
	return d.dropped()
}

// rowFixed is the width of everything in a request row except the path and the
// latency bar: the indent, the time, the method, the status and the latency,
// each padded to a fixed width and separated by a single space.
const rowFixed = 2 + 8 + 1 + 7 + 1 + 1 + 3 + 1 + 8 + 1

// Row column widths, in cells.
const (
	rowTimeWidth    = 8
	rowMethodWidth  = 7
	rowStatusWidth  = 3
	rowLatencyWidth = 8
	rowBarWidth     = 12
	rowMinPathWidth = 10
)

// rowLayout is how a request row is divided on a terminal of the given width.
type rowLayout struct {
	// path is how many cells the path may take, or 0 for all of it.
	path int
	// bar is how many cells the latency bar may take, or 0 for no bar.
	bar int
}

// layoutRow divides a row of the given width between the path and the bar, given
// the widest path on screen so every row lines up. The bar goes first when the
// terminal is narrow, because the numbers beside it carry more of the story
// than the shape of the bar does.
func layoutRow(width, longest int) rowLayout {
	if width <= 0 {
		return rowLayout{}
	}
	room := width - rowFixed - longest
	if room >= rowBarWidth+rowMinPathWidth {
		return rowLayout{path: longest, bar: rowBarWidth}
	}
	return rowLayout{path: min(longest, max(rowMinPathWidth, room)), bar: max(0, room-rowMinPathWidth)}
}

// requestRow lays one request out: when it finished, what it was asked for, the
// status it answered with, how long it took, and the latency as a bar against
// the slowest request on screen.
func requestRow(ev reqlog.Event, layout rowLayout, peak time.Duration) []segment {
	lat := ev.Latency
	if lat < 0 {
		lat = 0
	}

	segs := []segment{
		{"  ", stylePlain},
		{ev.Time.Format("15:04:05"), styleDim},
		{" ", stylePlain},
		{padRight(ev.Method, rowMethodWidth), stylePlain},
		{" ", stylePlain},
		{trimPath(pathOf(ev), layout.path), stylePlain},
		{" ", stylePlain},
		{padLeft(statusOf(ev), rowStatusWidth), statusStyle(ev.Status)},
		{" ", stylePlain},
		{padLeft(latency(lat), rowLatencyWidth), statusStyle(ev.Status)},
	}
	if n := barCells(lat, peak, layout.bar); n > 0 {
		segs = append(segs, segment{" ", stylePlain}, segment{strings.Repeat("█", n), statusStyle(ev.Status)})
	}
	if layout.bar == 0 && ev.Error != "" {
		// Only worth a column when there is no bar to explain it. In the
		// dashboard the bar and the red status are the signal on their own, and
		// the reason a request failed lives in the share's own log.
		segs = append(segs, segment{"  " + ev.Error, styleDim})
	}
	return segs
}

// RequestLine renders one request as a single line, which is what a pipe gets in
// place of a repainting screen. It is the same row the dashboard draws, without
// the bar, so the two agree on every number.
func (e *Env) RequestLine(ev reqlog.Event) string {
	var sb strings.Builder
	for _, seg := range requestRow(ev, rowLayout{}, 0) {
		sb.WriteString(paint(e, seg.style, seg.text))
	}
	return sb.String()
}

// streamRequests prints one line per request as it arrives, and ends when the
// context does or the log stops. history goes out first so a log piped into grep
// shows what was served before the pipe was attached too.
func (e *Env) streamRequests(ctx context.Context, history []reqlog.Event, events <-chan reqlog.Event) error {
	for _, ev := range history {
		fmt.Fprintln(e.Out, e.RequestLine(ev))
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			fmt.Fprintln(e.Out, e.RequestLine(ev))
		}
	}
}

// barCells is a latency as a run of blocks against the slowest latency in view.
// It never shrinks below one cell, because a request that was instant still
// happened and still deserves a tick on the chart.
func barCells(latency, peak time.Duration, width int) int {
	if width <= 0 || latency <= 0 {
		return width
	}
	n := width
	if peak > latency {
		n = int(float64(latency)/float64(peak)*float64(width) + 0.5)
	}
	return min(max(n, 1), width)
}

// statusOf renders the status code, or a dash for a request that never produced
// one because the handler panicked or the connection dropped.
func statusOf(ev reqlog.Event) string {
	if ev.Status == 0 {
		return "-"
	}
	return fmt.Sprintf("%d", ev.Status)
}

// statusStyle tints a request by how it went: a 2xx is the share doing its job,
// a 3xx is a redirect the caller will follow, a 4xx is the caller's problem, and
// anything else is the share's.
func statusStyle(status int) style {
	switch {
	case status == 0:
		return styleRed
	case status < 300:
		return styleGreen
	case status < 400:
		return styleCyan
	case status < 500:
		return styleYellow
	default:
		return styleRed
	}
}

// latency renders d in the largest unit that still fits the latency column, so a
// sub-millisecond request and a ten second one are both readable at a glance.
func latency(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.2fs", d.Seconds())
	case d >= 10*time.Millisecond:
		return fmt.Sprintf("%.0fms", ms)
	default:
		return fmt.Sprintf("%.1fms", ms)
	}
}

// slowest is the largest latency among the events, which is what every bar in
// the frame is measured against.
func slowest(events []reqlog.Event) time.Duration {
	peak := time.Duration(0)
	for _, ev := range events {
		if ev.Latency > peak {
			peak = ev.Latency
		}
	}
	return peak
}

// failures is how many requests did not go well: a 4xx, a 5xx, or no status at
// all.
func failures(events []reqlog.Event) int {
	n := 0
	for _, ev := range events {
		if ev.Status == 0 || ev.Status >= 400 {
			n++
		}
	}
	return n
}

// longestPath is the widest path among the events, so the columns line up.
func longestPath(events []reqlog.Event) int {
	longest := 0
	for _, ev := range events {
		if n := utf8.RuneCountInString(pathOf(ev)); n > longest {
			longest = n
		}
	}
	return longest
}

// pathOf is the path the client asked for, which is never empty but is quoted
// empty when a record somehow lost it.
func pathOf(ev reqlog.Event) string {
	if ev.Path == "" {
		return "/"
	}
	return ev.Path
}

// trimPath shortens p to at most n cells, keeping the end: a path that is too
// long is still recognisable by where it ends, so the head is what goes. A width
// of 0 means no limit.
func trimPath(p string, n int) string {
	if n <= 0 {
		return p
	}
	r := []rune(p)
	if len(r) <= n {
		return p
	}
	if n == 1 {
		return "…"
	}
	return "…" + string(r[len(r)-(n-1):])
}

// padLeft right aligns s in a column of n cells.
func padLeft(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// padRight left aligns s in a column of n cells.
func padRight(s string, n int) string {
	if d := n - utf8.RuneCountInString(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// plural renders a count with the noun agreeing with it.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
