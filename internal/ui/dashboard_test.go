package ui

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MandavkarPranjal/tailport/internal/reqlog"
)

func TestLatencyIsRenderedInTheLargestUnitThatStillFits(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{name: "under a millisecond keeps its fraction", in: 400 * time.Microsecond, want: "0.4ms"},
		{name: "a few milliseconds round to one decimal", in: 3 * time.Millisecond, want: "3.0ms"},
		{name: "tens of milliseconds drop the decimal", in: 47 * time.Millisecond, want: "47ms"},
		{name: "a slow request becomes seconds", in: 2500 * time.Millisecond, want: "2.50s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := latency(tt.in); got != tt.want {
				t.Errorf("latency(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStatusStyleTintsARequestByHowItWent(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   style
	}{
		{name: "a request that never answered is the share's problem", status: 0, want: styleRed},
		{name: "a 2xx is the share doing its job", status: 200, want: styleGreen},
		{name: "a 3xx is a redirect", status: 302, want: styleCyan},
		{name: "a 4xx is the caller's problem", status: 404, want: styleYellow},
		{name: "a 5xx is the share's problem", status: 502, want: styleRed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusStyle(tt.status); got != tt.want {
				t.Errorf("statusStyle(%d) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestStatusOfRendersADashWhenThereIsNoStatus(t *testing.T) {
	if got := statusOf(reqlog.Event{}); got != "-" {
		t.Errorf("statusOf(no status) = %q, want a dash", got)
	}
	if got := statusOf(reqlog.Event{Status: 404}); got != "404" {
		t.Errorf("statusOf(404) = %q, want 404", got)
	}
}

func TestBarCellsMeasuresAgainstTheSlowestRequestOnScreen(t *testing.T) {
	peak := 400 * time.Millisecond

	// The slowest request fills the bar, because it is what everything else is
	// measured against.
	if got := barCells(peak, peak, 12); got != 12 {
		t.Errorf("barCells(peak) = %d, want the whole bar", got)
	}
	if got := barCells(peak/2, peak, 12); got != 6 {
		t.Errorf("barCells(half) = %d, want 6", got)
	}
	// An instant request still happened, so it keeps one cell rather than
	// vanishing off the chart.
	if got := barCells(time.Microsecond, peak, 12); got != 1 {
		t.Errorf("barCells(instant) = %d, want 1", got)
	}
	// A request slower than the peak, which happens when the peak is only the
	// newest screenful, must not spill past the column.
	if got := barCells(time.Second, peak, 12); got != 12 {
		t.Errorf("barCells(over peak) = %d, want it capped at 12", got)
	}
	// No bar column means no bar at all, which is how the line stream asks.
	if got := barCells(peak, peak, 0); got != 0 {
		t.Errorf("barCells with no room = %d, want 0", got)
	}
}

// A latency that never got measured must not be drawn as the slowest request on
// screen. The chart exists to show which request was slowest, so an instant or
// unmeasured one claiming the full width is the chart lying about the thing it
// measures.
func TestBarCellsGivesAnUnmeasuredRequestTheSmallestBarNotTheLargest(t *testing.T) {
	peak := 400 * time.Millisecond

	for _, in := range []time.Duration{0, -time.Second} {
		if got := barCells(in, peak, 12); got != 1 {
			t.Errorf("barCells(%v) = %d cells, want 1: an unmeasured latency is not the slowest request", in, got)
		}
	}

	// A one-cell column still has to hold exactly one cell.
	if got := barCells(0, peak, 1); got != 1 {
		t.Errorf("barCells(0, peak, 1) = %d, want 1", got)
	}

	// And it must still lose to every request that did get measured, so it reads
	// as the quickest thing on screen rather than the slowest.
	instant := barCells(0, peak, 12)
	if measured := barCells(peak, peak, 12); instant >= measured {
		t.Errorf("an unmeasured request drew %d cells and the slowest real one drew %d, want the unmeasured one smaller",
			instant, measured)
	}
}

func TestLayoutRowGivesUpTheBarBeforeThePath(t *testing.T) {
	tests := []struct {
		name    string
		width   int
		longest int
		want    rowLayout
	}{
		{
			name:    "an unknown width leaves the path alone",
			width:   0,
			longest: 20,
			want:    rowLayout{},
		},
		{
			name:    "a wide terminal fits both and lines the paths up",
			width:   120,
			longest: 20,
			want:    rowLayout{path: 20, bar: rowBarWidth},
		},
		{
			name:    "a tight terminal shrinks the bar rather than the numbers",
			width:   rowFixed + 40 + 12,
			longest: 40,
			want:    rowLayout{path: 12, bar: 2},
		},
		{
			name:    "a terminal too small for the path drops the bar entirely",
			width:   rowFixed + 40 + 2,
			longest: 40,
			want:    rowLayout{path: rowMinPathWidth, bar: 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := layoutRow(tt.width, tt.longest); got != tt.want {
				t.Errorf("layoutRow(%d, %d) = %+v, want %+v", tt.width, tt.longest, got, tt.want)
			}
		})
	}
}

func TestLayoutRowNeverStarvesThePathOnAUsefulTerminal(t *testing.T) {
	// Any terminal that can show the fixed columns leaves at least the minimum
	// path width, so a request is never reduced to a status code.
	got := layoutRow(rowFixed+rowMinPathWidth+2, 200)
	if got.path < rowMinPathWidth {
		t.Errorf("layoutRow gave the path %d cells, want at least %d", got.path, rowMinPathWidth)
	}
	if got.bar > 2 {
		t.Errorf("layoutRow gave the bar %d cells, want the path to come first", got.bar)
	}
}

func TestRequestRowShowsTheTimeMethodPathStatusAndLatency(t *testing.T) {
	at := time.Date(2026, 3, 4, 9, 8, 7, 0, time.UTC)
	ev := reqlog.Event{Time: at, Method: "GET", Path: "/dashboard", Status: 200, Latency: 12 * time.Millisecond}

	row := rowText(requestRow(ev, rowLayout{path: 20, bar: 12}, 48*time.Millisecond))

	if !strings.HasPrefix(row, "  09:08:07 GET     /dashboard 200") {
		t.Errorf("row = %q, want the time, method and path in their own columns", row)
	}
	if !strings.Contains(row, "12ms") {
		t.Errorf("row = %q, want it to carry the latency", row)
	}
	// A 12ms request measured against a 48ms peak is a quarter of the bar.
	if got := strings.Count(row, "█"); got != 3 {
		t.Errorf("row drew %d bar cells, want 3 of 12", got)
	}
}

func TestRequestRowGrowsTheBarWithTheLatency(t *testing.T) {
	peak := time.Second
	slow := requestRow(reqlog.Event{Method: "GET", Status: 200, Latency: peak}, rowLayout{bar: 10}, peak)
	fast := requestRow(reqlog.Event{Method: "GET", Status: 200, Latency: peak / 5}, rowLayout{bar: 10}, peak)

	if strings.Count(rowText(slow), "█") <= strings.Count(rowText(fast), "█") {
		t.Errorf("the slow request drew %d cells and the fast one %d, want the slow one wider",
			strings.Count(rowText(slow), "█"), strings.Count(rowText(fast), "█"))
	}
}

// The clamp in requestRow turns a negative latency into zero before the bar is
// measured, so a record that lost its timing must still draw the smallest bar
// rather than the whole one.
func TestRequestRowDrawsAnUnmeasuredRequestWithASmallBar(t *testing.T) {
	peak := 400 * time.Millisecond

	unmeasured := rowText(requestRow(reqlog.Event{Method: "GET", Status: 200, Latency: -time.Second}, rowLayout{bar: 12}, peak))
	if got := strings.Count(unmeasured, "█"); got != 1 {
		t.Errorf("a request with no latency drew %d bar cells, want 1: it did not take the slowest", got)
	}

	// Nothing measurable should look like the slowest request on screen.
	slowest := rowText(requestRow(reqlog.Event{Method: "GET", Status: 200, Latency: peak}, rowLayout{bar: 12}, peak))
	if strings.Count(unmeasured, "█") >= strings.Count(slowest, "█") {
		t.Errorf("an unmeasured request drew as much as the slowest one:\n%s\n%s", unmeasured, slowest)
	}
}

func TestRequestRowTellsYouWhyARequestFailedWhenThereIsNoBar(t *testing.T) {
	ev := reqlog.Event{Method: "GET", Path: "/", Status: 502, Latency: time.Millisecond, Error: "connection refused"}

	// Without a bar column the reason is the only signal left, so it is worth
	// printing.
	if got := rowText(requestRow(ev, rowLayout{}, 0)); !strings.Contains(got, "connection refused") {
		t.Errorf("line row = %q, want it to name the failure", got)
	}
	// With a bar the dashboard has the red status and the shape already, and the
	// share's own log has the detail.
	if got := rowText(requestRow(ev, rowLayout{path: 5, bar: 8}, time.Millisecond)); strings.Contains(got, "connection refused") {
		t.Errorf("dashboard row = %q, want the error left out next to the bar", got)
	}
}

func TestRequestLineIsOneLinePerRequestWithEveryNumberOnIt(t *testing.T) {
	env, _, _ := newEnv(t, "")
	ev := reqlog.Event{
		Time:    time.Date(2026, 3, 4, 9, 8, 7, 0, time.UTC),
		Method:  "POST",
		Path:    "/api/thing",
		Status:  500,
		Latency: 1500 * time.Millisecond,
		Error:   "upstream hung up",
	}

	got := env.RequestLine(ev)

	want := "  09:08:07 POST    /api/thing 500    1.50s  upstream hung up"
	if got != want {
		t.Errorf("RequestLine =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("RequestLine = %q, want a single line a pipe can read", got)
	}
}

func TestStreamRequestsPrintsTheHistoryAndThenWhatArrives(t *testing.T) {
	env, out, _ := newEnv(t, "")

	history := []reqlog.Event{{Method: "GET", Path: "/before", Status: 200}}
	events := make(chan reqlog.Event, 1)
	events <- reqlog.Event{Method: "GET", Path: "/after", Status: 200}
	close(events)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := env.Dashboard(ctx, "port 3000", history, events, nil); err != nil {
		t.Fatalf("Dashboard error: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "/before") {
		t.Errorf("output = %q, want the requests the log already held", got)
	}
	if !strings.Contains(got, "/after") {
		t.Errorf("output = %q, want the request that arrived while watching", got)
	}
	// A pipe gets plain lines, never a repaint escape.
	if strings.Contains(got, "\x1b[") {
		t.Errorf("output = %q, want no terminal escapes when the output is not a terminal", got)
	}
	if n := strings.Count(strings.TrimRight(got, "\n"), "\n"); n != 1 {
		t.Errorf("output has %d line breaks, want one line per request:\n%q", n, got)
	}
}

func TestDashboardStopsWhenTheContextEnds(t *testing.T) {
	env, _, _ := newEnv(t, "")

	ctx, cancel := context.WithCancel(t.Context())
	// A share that keeps serving after the watcher walks away: the dashboard has
	// to give the terminal back rather than waiting for the log to run dry.
	events := make(chan reqlog.Event)
	go func() {
		cancel()
		time.AfterFunc(time.Hour, func() { close(events) })
	}()

	done := make(chan error, 1)
	go func() { done <- env.Dashboard(ctx, "port 3000", nil, events, nil) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Dashboard error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dashboard did not return after the context ended")
	}
}

func TestDashboardPaintsAFrameWithAHeaderRowsAndAFooter(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{
		env:    env,
		title:  "port 3000  host",
		fd:     -1, // no terminal, so refreshSize leaves the size alone
		width:  100,
		height: 12,
	}
	d.push([]reqlog.Event{
		{Method: "GET", Path: "/a", Status: 200, Latency: 5 * time.Millisecond},
		{Method: "GET", Path: "/b", Status: 502, Latency: 30 * time.Millisecond},
	})

	d.render()
	frame := lastDashboardFrame(out)

	if !strings.Contains(frame, "port 3000  host") {
		t.Errorf("frame = %q, want the title", frame)
	}
	if !strings.Contains(frame, "2 requests") {
		t.Errorf("frame = %q, want the request count", frame)
	}
	if !strings.Contains(frame, "1 failure") {
		t.Errorf("frame = %q, want the failure count that is the reason to be watching", frame)
	}
	if !strings.Contains(frame, "/a") || !strings.Contains(frame, "/b") {
		t.Errorf("frame = %q, want a row per request", frame)
	}
	if !strings.Contains(frame, "c to clear") {
		t.Errorf("frame = %q, want the key map", frame)
	}
	if d.drawn != strings.Count(frame, "\r\n") {
		t.Errorf("drawn = %d, want it to count the %d lines just painted", d.drawn, strings.Count(frame, "\r\n"))
	}
}

func TestDashboardRepaintsOverTheLastFrame(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{env: env, title: "port 3000", fd: -1, width: 100, height: 12}
	d.push([]reqlog.Event{{Method: "GET", Path: "/a", Status: 200}})

	d.render()
	first := lastDashboardFrame(out)
	if strings.Contains(first, "\x1b[") {
		t.Errorf("the first frame used escapes %q, want none before anything is drawn", first)
	}

	// The second frame has to step back over every line of the first, or the log
	// scrolls and the newest request walks off the screen.
	d.render()
	if want := fmt.Sprintf("\x1b[%dA\r\x1b[J", d.drawn); !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want the second frame to step back with %q", out.String(), want)
	}
	if got := lastDashboardFrame(out); strings.Contains(got, "\x1b[") {
		t.Errorf("second frame = %q, want it to start after the repaint", got)
	}
}

func TestDashboardShowsTheNewestRequestsThatFit(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{env: env, title: "port 3000", fd: -1, width: 100, height: dashChrome + 2}
	var all []reqlog.Event
	for i := range 5 {
		all = append(all, reqlog.Event{Method: "GET", Path: "/p" + string(rune('a'+i)), Status: 200})
	}
	d.push(all)

	d.render()
	frame := lastDashboardFrame(out)

	if strings.Contains(frame, "/pa") {
		t.Errorf("frame = %q, want the oldest requests scrolled off", frame)
	}
	if !strings.Contains(frame, "/pd") || !strings.Contains(frame, "/pe") {
		t.Errorf("frame = %q, want the two newest requests", frame)
	}
}

func TestBarsAreScaledAgainstTheSlowestRequestOnScreen(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{env: env, title: "port 3000", fd: -1, width: 100, height: dashChrome + 3}
	// The slow one has scrolled off, so nobody watching can see why the bars are
	// the width they are.
	d.push([]reqlog.Event{
		{Method: "GET", Path: "/gone", Status: 200, Latency: 10 * time.Second},
		{Method: "GET", Path: "/a", Status: 200, Latency: 10 * time.Millisecond},
		{Method: "GET", Path: "/b", Status: 200, Latency: 5 * time.Millisecond},
		{Method: "GET", Path: "/c", Status: 200, Latency: 10 * time.Millisecond},
	})

	d.render()
	frame := lastDashboardFrame(out)

	if strings.Contains(frame, "/gone") {
		t.Fatalf("frame = %q, want the slow request scrolled off", frame)
	}
	// Measured against the slowest of the three on screen, every bar is at least
	// half the width; measured against a request nobody can see, all three would
	// shrink to a single cell and look like the same very slow request.
	rows := barWidthsIn(frame)
	if len(rows) != 3 {
		t.Fatalf("frame = %q, want the three requests on screen, got bars %v", frame, rows)
	}
	for i, got := range rows {
		if got < rowBarWidth/2 {
			t.Errorf("bar %d drew %d cells, want a bar measured against the requests on screen", i, got)
		}
	}
	// The header still reports the slowest overall, which is the number that
	// makes a scrolled-off slowness visible at all.
	if !strings.Contains(frame, "slowest 10.00s") {
		t.Errorf("frame = %q, want the header to still name the slowest overall", frame)
	}
}

func TestDashboardSaysItIsWaitingBeforeAnyRequestArrives(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{env: env, title: "port 3000", fd: -1, width: 100, height: 12}

	d.render()
	frame := lastDashboardFrame(out)

	if !strings.Contains(frame, "waiting for requests...") {
		t.Errorf("frame = %q, want it to say nothing has been served yet", frame)
	}
	if !strings.Contains(frame, "live") {
		t.Errorf("frame = %q, want the header to say the log is live", frame)
	}
}

func TestDashboardSaysWhenTheLogCouldNotKeepUp(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{
		env:     env,
		title:   "port 3000",
		fd:      -1,
		width:   100,
		height:  12,
		dropped: func() int { return 3 },
	}
	d.push([]reqlog.Event{{Method: "GET", Path: "/a", Status: 200}})

	d.render()

	// Showing fewer requests than were served without saying so would make the
	// log disagree with what callers saw.
	if got := lastDashboardFrame(out); !strings.Contains(got, "3 requests not shown") {
		t.Errorf("frame = %q, want it to admit what it dropped", got)
	}
}

func TestDashboardSaysNothingAboutDroppedRequestsWhenItKnowsOfNone(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{env: env, title: "port 3000", fd: -1, width: 100, height: 12}
	d.push([]reqlog.Event{{Method: "GET", Path: "/a", Status: 200}})

	d.render()

	if got := lastDashboardFrame(out); strings.Contains(got, "not shown") {
		t.Errorf("frame = %q, want no mention of a drop that did not happen", got)
	}
}

func TestPushForgetsTheOldestRequestsOnceTheLogIsFull(t *testing.T) {
	d := &dashboard{}
	d.push(make([]reqlog.Event, maxKept))
	if len(d.events) != maxKept {
		t.Fatalf("len(events) = %d, want %d", len(d.events), maxKept)
	}

	d.push([]reqlog.Event{{Path: "/newest"}})

	if len(d.events) != maxKept {
		t.Errorf("len(events) = %d, want it capped at %d", len(d.events), maxKept)
	}
	if got := d.events[maxKept-1].Path; got != "/newest" {
		t.Errorf("the newest event's path = %q, want the one just pushed", got)
	}
	// The oldest event falls off the front rather than the newest being lost.
	if got := d.events[0].Path; got != "" {
		t.Errorf("events[0].Path = %q, want the oldest event dropped", got)
	}
}

func TestClearThrowsAwayTheLogButKeepsWatching(t *testing.T) {
	d := &dashboard{}
	d.push([]reqlog.Event{{Path: "/a"}, {Path: "/b"}})

	d.clear()

	if len(d.events) != 0 {
		t.Errorf("len(events) = %d, want the log cleared", len(d.events))
	}
	// Clearing is not quitting: the next request still shows up.
	d.push([]reqlog.Event{{Path: "/c"}})
	if len(d.events) != 1 {
		t.Errorf("len(events) = %d, want watching to continue after a clear", len(d.events))
	}
}

func TestKeyClearsTheLogAndQuitAsksTheDashboardToClose(t *testing.T) {
	d := &dashboard{}
	d.push([]reqlog.Event{{Path: "/a"}})

	if d.key(key{kind: keyRune, r: 'c'}) {
		t.Error("pressing c should keep the dashboard open")
	}
	if len(d.events) != 0 {
		t.Errorf("len(events) = %d, want pressing c to clear the log", len(d.events))
	}

	if !d.key(key{kind: keyQuit}) {
		t.Error("ctrl-c should close the dashboard")
	}
}

// The footer names q as a way out, so pressing it has to be one. Reading a hint
// that does nothing leaves the log scrolling and the user convinced it is stuck.
func TestKeyQuitsOnQ(t *testing.T) {
	for _, r := range []rune{'q', 'Q'} {
		d := &dashboard{}
		if !d.key(key{kind: keyRune, r: r}) {
			t.Errorf("pressing %q should close the dashboard", r)
		}
	}
}

// The bug this covers lived between decoding a key and acting on it: readKey
// hands back a plain q as an ordinary rune, and nothing downstream was looking
// for it. Going through readKey keeps that gap from reopening.
func TestQuittingFromRealTypedInput(t *testing.T) {
	for _, tt := range []struct {
		name  string
		input string
	}{
		{name: "a typed q", input: "q"},
		{name: "a typed capital Q", input: "Q"},
		{name: "ctrl-c", input: "\x03"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := &dashboard{}
			d.push([]reqlog.Event{{Method: "GET", Path: "/a", Status: 200}})

			k, err := readKey(bufio.NewReader(strings.NewReader(tt.input)))
			if err != nil {
				t.Fatalf("readKey(%q) error: %v", tt.input, err)
			}
			if !d.key(k) {
				t.Errorf("typing %q left the dashboard open, want it to stop", tt.input)
			}
		})
	}
}

// Every key the footer offers has to be one the dashboard answers, so the hint
// cannot quietly stop matching the code.
func TestFooterOnlyOffersKeysThatWork(t *testing.T) {
	d := &dashboard{title: "port 3000"}
	d.push([]reqlog.Event{{Method: "GET", Path: "/a", Status: 200}})

	hint := rowText(d.footer())

	if !strings.Contains(hint, "q") {
		t.Fatalf("footer = %q, want it to offer a plain q to stop with", hint)
	}
	if !strings.Contains(hint, "c to clear") {
		t.Errorf("footer = %q, want it to offer c to clear", hint)
	}

	// The offer to clear has to reach the log, and must not take the dashboard
	// down with it.
	if d.key(key{kind: keyRune, r: 'c'}) {
		t.Error("pressing c stopped the dashboard, want it to keep watching")
	}
	if len(d.events) != 0 {
		t.Errorf("len(events) = %d, want the footer offer to clear to reach the log", len(d.events))
	}

	if !d.key(key{kind: keyRune, r: 'q'}) {
		t.Error("the footer offers q, but pressing q does nothing")
	}
}

func TestAbsorbTakesEveryRequestThatHasAlreadyArrived(t *testing.T) {
	// A burst of requests should cost one repaint, not one per request, or a
	// busy share would spend all its time painting.
	events := make(chan reqlog.Event, 5)
	for i := range 5 {
		events <- reqlog.Event{Path: string(rune('a' + i))}
	}

	d := &dashboard{}
	d.absorb(events)

	if len(d.events) != 5 {
		t.Errorf("len(events) = %d, want all 5 taken at once", len(d.events))
	}
}

func TestWindowKeepsTheFrameShorterThanTheTerminal(t *testing.T) {
	tests := []struct {
		name   string
		height int
		events int
		want   int
	}{
		{name: "an unknown size shows everything", height: 0, events: 9, want: 9},
		{name: "a tall terminal still only shows what there is", height: 40, events: 3, want: 3},
		{name: "a short terminal keeps a row, so the frame is never empty", height: 1, events: 9, want: 1},
		{name: "a normal terminal leaves room for the chrome", height: 12, events: 9, want: 9},
		{name: "a normal terminal clips the log to the rows it has", height: 10, events: 20, want: 10 - dashChrome},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &dashboard{height: tt.height, events: make([]reqlog.Event, tt.events)}
			if got := d.window(); got != tt.want {
				t.Errorf("window() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTrimPathKeepsTheEndWhereAPathIsStillRecognisable(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "no limit leaves the path alone", in: "/a/very/long/path", n: 0, want: "/a/very/long/path"},
		{name: "a path that fits is untouched", in: "/api", n: 10, want: "/api"},
		{name: "a long path keeps its tail", in: "/a/very/long/path", n: 6, want: "…/path"},
		{name: "a single cell still shows that it was cut", in: "/a/very/long/path", n: 1, want: "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trimPath(tt.in, tt.n); got != tt.want {
				t.Errorf("trimPath(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

func TestColumnsPadByRunesSoAMultiBytePathStillLinesUp(t *testing.T) {
	if got := padRight("héllo", 8); got != "héllo   " {
		t.Errorf("padRight = %q, want three spaces, not five", got)
	}
	if got := padLeft("héllo", 8); got != "   héllo" {
		t.Errorf("padLeft = %q, want three spaces, not five", got)
	}
	if got := padRight("something long", 4); got != "something long" {
		t.Errorf("padRight of an overlong string = %q, want it left whole", got)
	}
}

func TestHeaderCountsTheRequestsTheWorstOneAndWhatFailed(t *testing.T) {
	d := &dashboard{title: "port 3000"}
	d.push([]reqlog.Event{
		{Method: "GET", Status: 200, Latency: 5 * time.Millisecond},
		{Method: "GET", Status: 404, Latency: 15 * time.Millisecond},
		{Method: "GET", Status: 502, Latency: 20 * time.Millisecond},
		{Method: "GET", Latency: 10 * time.Millisecond}, // a request that never answered
	})

	got := rowText(d.header())

	if !strings.Contains(got, "4 requests") {
		t.Errorf("header = %q, want all 4 requests counted", got)
	}
	if !strings.Contains(got, "slowest 20ms") {
		t.Errorf("header = %q, want the slowest request", got)
	}
	if !strings.Contains(got, "3 failures") {
		t.Errorf("header = %q, want the 5xx, the 4xx and the request with no status", got)
	}
}

func TestHeaderSaysItIsLiveBeforeAnythingHasHappened(t *testing.T) {
	d := &dashboard{title: "port 3000"}
	got := rowText(d.header())

	if !strings.Contains(got, "live") {
		t.Errorf("header = %q, want it to say the log is live", got)
	}
	if strings.Contains(got, "request") {
		t.Errorf("header = %q, want no count before there is anything to count", got)
	}
}

func TestPluralAgreesWithTheCount(t *testing.T) {
	tests := []struct {
		n    int
		noun string
		want string
	}{
		{n: 0, noun: "request", want: "0 requests"},
		{n: 1, noun: "request", want: "1 request"},
		{n: 2, noun: "request", want: "2 requests"},
		{n: 1, noun: "failure", want: "1 failure"},
	}
	for _, tt := range tests {
		if got := plural(tt.n, tt.noun); got != tt.want {
			t.Errorf("plural(%d, %q) = %q, want %q", tt.n, tt.noun, got, tt.want)
		}
	}
}

// barWidthsIn returns how many cells each latency bar on the frame took, in the
// order they were painted.
func barWidthsIn(frame string) []int {
	var widths []int
	for _, line := range strings.Split(frame, "\r\n") {
		if n := strings.Count(line, "█"); n > 0 {
			widths = append(widths, n)
		}
	}
	return widths
}

// rowText paints segments as one plain string, the way a pipe sees a row.
func rowText(segs []segment) string {
	var sb strings.Builder
	for _, seg := range segs {
		sb.WriteString(seg.text)
	}
	return sb.String()
}

// lastDashboardFrame returns everything after the last repaint, which is the
// frame currently on screen. Before the first repaint it is all of the output.
func lastDashboardFrame(out *bytes.Buffer) string {
	s := out.String()
	if at := strings.LastIndex(s, "\x1b[J"); at >= 0 {
		return s[at+len("\x1b[J"):]
	}
	return s
}

func TestLongestPathMeasuresInCellsSoWidePathsStillLineUp(t *testing.T) {
	events := []reqlog.Event{
		{Path: "/ab"},  // 3 cells
		{Path: "/日本語"}, // 7 cells
	}
	if got, want := longestPath(events), 7; got != want {
		t.Errorf("longestPath = %d, want %d cells", got, want)
	}
}

func TestTrimPathKeepsATailThatActuallyFitsTheColumn(t *testing.T) {
	// Measured in cells, the tail has to be found by walking backwards, since
	// counting runes off the end would overrun the column and wrap the row.
	got := trimPath("/日本語/詳細", 5)
	if cells(got) > 5 {
		t.Errorf("trimPath = %q takes %d cells, want at most 5", got, cells(got))
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("trimPath = %q, want the head replaced by an ellipsis", got)
	}
	// A path that already fits is left exactly as it is.
	if got := trimPath("/日本語", 99); got != "/日本語" {
		t.Errorf("trimPath of a short path = %q, want it whole", got)
	}
}

func TestAPathFromAClientCannotDriveTheWatchersTerminal(t *testing.T) {
	env, _, _ := newEnv(t, "")
	ev := reqlog.Event{Method: "GET", Path: "/\x1b[2J\x1b[H", Status: 200, Latency: time.Millisecond}

	// The escape belongs to the client. A watcher is a terminal, so obeying it
	// would let anybody who can make a request wipe the screen or repaint it.
	if got := env.RequestLine(ev); strings.Contains(got, "\x1b") {
		t.Errorf("RequestLine = %q, want the escape shown rather than passed on", got)
	}
	if got := rowText(requestRow(ev, rowLayout{}, 0)); strings.Contains(got, "\x1b") {
		t.Errorf("row = %q, want the escape shown rather than painted", got)
	}
}

func TestAPathThatDecodesToANewlineStaysOnOneLine(t *testing.T) {
	env, _, _ := newEnv(t, "")
	// net/http decodes %0A in the request target into a real newline, so this is
	// a path a client really can ask for.
	ev := reqlog.Event{Method: "GET", Path: "/a\nb", Status: 200}

	got := env.RequestLine(ev)

	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Errorf("RequestLine = %q, want one line per request however the client spelled it", got)
	}
	if !strings.Contains(got, `\n`) {
		t.Errorf("RequestLine = %q, want the newline spelled out", got)
	}
}

func TestEveryRequestFieldIsEscapedAndNotJustThePath(t *testing.T) {
	tests := []struct {
		name string
		ev   reqlog.Event
		want string
	}{
		{
			name: "a method carrying an escape",
			ev:   reqlog.Event{Method: "GE\x1b[31mT", Path: "/", Status: 200},
			want: `\x1b`,
		},
		{
			name: "a carriage return in the method",
			ev:   reqlog.Event{Method: "GET\r", Path: "/", Status: 200},
			want: `\r`,
		},
		{
			name: "a delete character in the method",
			ev:   reqlog.Event{Method: "GET\x7f", Path: "/", Status: 200},
			want: `\x7f`,
		},
		{
			name: "an error carrying a newline",
			ev: reqlog.Event{
				Method: "GET", Path: "/", Status: 502,
				Latency: time.Millisecond, Error: "connection refused\nGET /admin",
			},
			want: `\n`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _, _ := newEnv(t, "")

			got := env.RequestLine(tt.ev)

			if !strings.Contains(got, tt.want) {
				t.Errorf("RequestLine = %q, want the control character spelled out as %q", got, tt.want)
			}
			if strings.ContainsAny(got, "\x1b\n\r\x7f") {
				t.Errorf("RequestLine = %q, want no control characters left in it", got)
			}
		})
	}
}

func TestColumnsLineUpWhenAPathIsLongerOnceItIsEscaped(t *testing.T) {
	plain := reqlog.Event{Method: "GET", Path: "/abcd", Status: 200, Latency: time.Millisecond}
	nasty := reqlog.Event{Method: "GET", Path: "/\x1b[31m", Status: 200, Latency: time.Millisecond}
	shown := []reqlog.Event{plain, nasty}

	// The layout divides the terminal by the widest path on screen, so it has to
	// measure what gets printed. The escape turns a five byte path into nine
	// printed characters, and measuring the raw bytes would push the status and
	// latency columns out past where they belong.
	row := rowText(requestRow(nasty, layoutRow(120, longestPath(shown)), time.Millisecond))

	if want := rowFixed + cells(printable(nasty.Path)) + rowBarWidth; cells(row) != want {
		t.Errorf("row is %d cells wide, want %d so the columns still line up", cells(row), want)
	}
}

func TestABidiOverrideCannotDisguiseWhatWasRequested(t *testing.T) {
	env, _, _ := newEnv(t, "")
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "a right to left override reorders what is drawn", in: "/\u202egnp.exe", want: `\u202e`},
		{name: "a left to right embed", in: "/\u202aetc", want: `\u202a`},
		{name: "an isolate", in: "/\u2066etc", want: `\u2066`},
		{name: "a pop directional format", in: "/\u202cetc", want: `\u202c`},
		{name: "a left to right mark", in: "/\u200eetc", want: `\u200e`},
		{name: "an arabic letter mark", in: "/\u061cetc", want: `\u061c`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := reqlog.Event{Method: "GET", Path: tt.in, Status: 200}

			got := env.RequestLine(ev)

			// A mark draws nothing, so it can reorder the characters around it
			// without looking like it did anything at all.
			if !strings.Contains(got, tt.want) {
				t.Errorf("RequestLine = %q, want the override spelled out as %q", got, tt.want)
			}
			if strings.ContainsRune(got, '\u202e') {
				t.Errorf("RequestLine = %q, want no bidi override left to act on", got)
			}
		})
	}
}

func TestABidiOverrideIsEscapedInTheMethodAndTheErrorToo(t *testing.T) {
	env, _, _ := newEnv(t, "")
	tests := []struct {
		name string
		ev   reqlog.Event
	}{
		{name: "a method", ev: reqlog.Event{Method: "GET\u202eX", Path: "/", Status: 200}},
		{
			name: "an error",
			ev: reqlog.Event{
				Method: "GET", Path: "/", Status: 502,
				Latency: time.Millisecond, Error: "refused\u202eexe",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := env.RequestLine(tt.ev); strings.ContainsRune(got, '\u202e') {
				t.Errorf("RequestLine = %q, want the override spelled out", got)
			}
		})
	}
}

func TestANoticeFromTheShareIsShownRatherThanCountedAsARequest(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{env: env, title: "port 3000", fd: -1, width: 100, height: 12}
	d.push([]reqlog.Event{
		{Method: "GET", Path: "/a", Status: 200, Latency: 5 * time.Millisecond},
		{Notice: "42 requests were not recorded"},
		{Method: "GET", Path: "/b", Status: 200, Latency: 5 * time.Millisecond},
	})

	d.render()
	frame := lastDashboardFrame(out)

	if !strings.Contains(frame, "42 requests were not recorded") {
		t.Errorf("frame = %q, want the share's notice about its own log", frame)
	}
	// The notice is about the log, so it must not inflate the request count or
	// push a row off the screen.
	if !strings.Contains(frame, "2 requests") {
		t.Errorf("frame = %q, want only the two real requests counted", frame)
	}
	if !strings.Contains(frame, "/a") || !strings.Contains(frame, "/b") {
		t.Errorf("frame = %q, want both requests still on screen", frame)
	}
	// Rows are written with an explicit carriage return, so look for a whole line
	// rather than a trailing newline that no line here ever ends with.
	for _, line := range strings.Split(frame, "\r\n") {
		if line == "42 requests were not recorded" {
			t.Errorf("frame = %q, want the notice on the header rather than as a row of its own", frame)
		}
	}
}

func TestANoticeIsShownBeforeAnyRequestArrives(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{env: env, title: "port 3000", fd: -1, width: 100, height: 12}
	d.push([]reqlog.Event{{Notice: "12 requests were not recorded"}})

	d.render()
	frame := lastDashboardFrame(out)

	// The header has a "live" placeholder for a log with nothing in it, and that
	// must not swallow a notice, which is the most important thing on the line
	// when the share is dropping what it serves.
	if !strings.Contains(frame, "12 requests were not recorded") {
		t.Errorf("frame = %q, want the notice even though nothing has been served", frame)
	}
}

func TestANewerNoticeReplacesTheOlderOne(t *testing.T) {
	env, out, _ := newEnv(t, "")
	d := &dashboard{env: env, title: "port 3000", fd: -1, width: 100, height: 12}
	d.push([]reqlog.Event{{Notice: "12 requests were not recorded"}})
	d.push([]reqlog.Event{{Method: "GET", Status: 200}})

	d.render()
	d.push([]reqlog.Event{{Notice: "30 requests were not recorded"}})
	d.render()
	frame := lastDashboardFrame(out)

	if strings.Contains(frame, "12 requests were not recorded") {
		t.Errorf("frame = %q, want the stale notice gone", frame)
	}
	if !strings.Contains(frame, "30 requests were not recorded") {
		t.Errorf("frame = %q, want the current notice", frame)
	}
}

func TestRequestLinePrintsANoticeOnItsOwn(t *testing.T) {
	env, _, _ := newEnv(t, "")

	got := env.RequestLine(reqlog.Event{Notice: "42 requests were not recorded"})

	// Laid out as a request this would claim a method, a path and a 200 that
	// never existed, and `watch | grep 502` would count it as a served request.
	if got != "42 requests were not recorded" {
		t.Errorf("RequestLine = %q, want the notice and nothing else", got)
	}
}

func TestANoticeStillReachesAPipedWatcher(t *testing.T) {
	env, out, _ := newEnv(t, "")
	events := make(chan reqlog.Event, 1)
	events <- reqlog.Event{Notice: "7 requests were not recorded"}
	close(events)

	if err := env.streamRequests(t.Context(), nil, events); err != nil {
		t.Fatalf("streamRequests() error: %v", err)
	}

	if got := strings.TrimSpace(out.String()); got != "7 requests were not recorded" {
		t.Errorf("output = %q, want the notice as its own line", got)
	}
}
