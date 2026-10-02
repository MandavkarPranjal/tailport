package ui

import (
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
		t.Error("keyQuit should close the dashboard")
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
