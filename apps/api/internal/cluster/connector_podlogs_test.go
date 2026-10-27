package cluster

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// kubeletLines builds what the kubelet streams with Timestamps=true and a
// SinceTime: one line per second from `from` to `to`, oldest first.
func kubeletLines(from, to time.Time, msg func(time.Time) string) string {
	var b strings.Builder
	for t := from; !t.After(to); t = t.Add(time.Second) {
		fmt.Fprintf(&b, "%s %s\n", t.Format(time.RFC3339Nano), msg(t))
	}
	return b.String()
}

func TestPodLogOptionsNoKubeletTailWithEndTime(t *testing.T) {
	since := time.Date(2026, 10, 9, 23, 3, 0, 0, time.UTC)

	windowed := podLogOptions(LogQuery{TailLines: 200, SinceTime: since, EndTime: since.Add(9 * time.Minute)})
	if windowed.TailLines != nil {
		t.Errorf("a closed window sent TailLines=%d to the kubelet: it counts back from now, not from endTime", *windowed.TailLines)
	}
	if windowed.SinceTime == nil || !windowed.SinceTime.Time.Equal(since) {
		t.Errorf("SinceTime = %v, want %v", windowed.SinceTime, since)
	}
	if !windowed.Timestamps {
		t.Error("a closed window needs timestamps to find endTime")
	}

	open := podLogOptions(LogQuery{TailLines: 200, SinceTime: since})
	if open.TailLines == nil || *open.TailLines != 200 {
		t.Errorf("without endTime the tail goes to the kubelet as before, got %v", open.TailLines)
	}
}

// The lab case: an incident at 23:08:40–23:08:50Z, a window of 23:03–23:12Z,
// asked at 23:29Z. The kubelet used to return the last 200 lines up to now
// (23:21–23:29Z), all past endTime, so the window came back empty.
func TestReadLogWindowOldWindowIsNotEmpty(t *testing.T) {
	since := time.Date(2026, 10, 9, 23, 3, 0, 0, time.UTC)
	end := time.Date(2026, 10, 9, 23, 12, 0, 0, time.UTC)
	now := time.Date(2026, 10, 9, 23, 29, 0, 0, time.UTC)
	errFrom := time.Date(2026, 10, 9, 23, 8, 40, 0, time.UTC)
	errTo := time.Date(2026, 10, 9, 23, 8, 50, 0, time.UTC)
	stream := kubeletLines(since, now, func(t time.Time) string {
		if !t.Before(errFrom) && !t.After(errTo) {
			return "[orders-api] ERROR GET / -> 500 query failed: the database system is shutting down (SQLSTATE 57P03)"
		}
		return "[orders-api] INFO GET / -> 200 rows=4"
	})

	t.Run("whole window", func(t *testing.T) {
		logs, win, err := readLogWindow(strings.NewReader(stream), end, 0)
		if err != nil {
			t.Fatal(err)
		}
		// 23:03:00 → 23:12:02 inclusive (2 s skew) = 543 lines.
		if win.Lines != 543 || win.Dropped != 0 || !win.PastEnd || !win.Bounded {
			t.Errorf("window = %+v, want 543 lines, none dropped, past end", win)
		}
		if got := strings.Count(logs, "57P03"); got != 11 {
			t.Errorf("the incident's 11 error lines are not all there: %d", got)
		}
		if strings.Contains(logs, "T23:12:03") || strings.Contains(logs, "T23:21:") {
			t.Error("lines past endTime leaked into the window")
		}
	})

	t.Run("tail applies inside the window", func(t *testing.T) {
		logs, win, err := readLogWindow(strings.NewReader(stream), end, 200)
		if err != nil {
			t.Fatal(err)
		}
		if win.Lines != 543 || win.Dropped != 343 {
			t.Errorf("window = %+v, want 543 lines with 343 dropped", win)
		}
		lines := strings.Split(strings.TrimSuffix(logs, "\n"), "\n")
		if len(lines) != 200 {
			t.Fatalf("returned %d lines, want 200", len(lines))
		}
		if !strings.HasPrefix(lines[0], "2026-10-09T23:08:43Z") || !strings.HasPrefix(lines[199], "2026-10-09T23:12:02Z") {
			t.Errorf("want the newest 200 lines of the window in order, got %q … %q", lines[0][:20], lines[199][:20])
		}
	})
}

func TestReadLogWindowEmptyKinds(t *testing.T) {
	end := time.Date(2026, 10, 9, 23, 12, 0, 0, time.UTC)

	// Nothing in the window, the log goes on after it.
	after := kubeletLines(end.Add(time.Minute), end.Add(2*time.Minute), func(time.Time) string { return "x" })
	logs, win, err := readLogWindow(strings.NewReader(after), end, 200)
	if err != nil || logs != "" || win.Lines != 0 || !win.PastEnd {
		t.Errorf("empty window before later lines: logs=%q win=%+v err=%v", logs, win, err)
	}

	// Nothing at all.
	logs, win, err = readLogWindow(strings.NewReader(""), end, 200)
	if err != nil || logs != "" || win.Lines != 0 || win.PastEnd {
		t.Errorf("empty log: logs=%q win=%+v err=%v", logs, win, err)
	}
}

// failingReader yields data, then fails the way a stream past its deadline does.
type failingReader struct {
	data string
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.data == "" {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

// The kubelet streams oldest first: a window bigger than the byte cap loses
// its END, and must say so instead of passing for the whole window.
func TestReadLogWindowCapped(t *testing.T) {
	since := time.Date(2026, 10, 9, 23, 0, 0, 0, time.UTC)
	end := since.Add(10 * time.Minute)
	stream := kubeletLines(since, end.Add(time.Minute), func(time.Time) string { return "[orders-api] INFO GET / -> 200" })
	lineLen := len(strings.SplitAfter(stream, "\n")[0]) // every line has the same length

	t.Run("cap mid-line", func(t *testing.T) {
		max := int64(100*lineLen + lineLen/2) // 100 lines and half of the 101st
		logs, win, err := readLogWindowCapped(strings.NewReader(stream), end, 0, max)
		if err != nil {
			t.Fatal(err)
		}
		if !win.Capped || win.PastEnd || win.Lines != 100 {
			t.Errorf("window = %+v, want capped at 100 whole lines", win)
		}
		if got := strings.Count(logs, "\n"); got != 100 || !strings.HasSuffix(logs, "-> 200\n") {
			t.Errorf("the half line must be dropped: %d lines, ends %q", got, logs[len(logs)-20:])
		}
		// Line 100 is 23:01:39: where a caller reads on from.
		if want := since.Add(99 * time.Second); !win.ReadUntil.Equal(want) && !win.ReadUntil.Equal(want.Add(time.Second)) {
			t.Errorf("ReadUntil = %v, want about %v", win.ReadUntil, want)
		}
	})

	t.Run("cap on a line boundary keeps every line", func(t *testing.T) {
		logs, win, err := readLogWindowCapped(strings.NewReader(stream), end, 0, int64(100*lineLen))
		if err != nil {
			t.Fatal(err)
		}
		if !win.Capped || win.Lines != 100 || strings.Count(logs, "\n") != 100 {
			t.Errorf("window = %+v, %d lines", win, strings.Count(logs, "\n"))
		}
	})

	t.Run("tail over a capped read", func(t *testing.T) {
		logs, win, err := readLogWindowCapped(strings.NewReader(stream), end, 20, int64(100*lineLen+lineLen/2))
		if err != nil {
			t.Fatal(err)
		}
		if !win.Capped || win.Lines != 100 || win.Dropped != 80 || strings.Count(logs, "\n") != 20 {
			t.Errorf("window = %+v, %d lines", win, strings.Count(logs, "\n"))
		}
	})

	t.Run("a window that fits is not capped", func(t *testing.T) {
		_, win, err := readLogWindowCapped(strings.NewReader(stream), end, 0, int64(len(stream)))
		if err != nil {
			t.Fatal(err)
		}
		if win.Capped || !win.PastEnd || !win.ReadUntil.IsZero() {
			t.Errorf("window = %+v, want the whole window, past end, not capped", win)
		}
	})
}

func TestReadLogWindowInterrupted(t *testing.T) {
	since := time.Date(2026, 10, 9, 23, 0, 0, 0, time.UTC)
	end := since.Add(10 * time.Minute)
	lines := kubeletLines(since, since.Add(49*time.Second), func(time.Time) string { return "x" })
	deadline := fmt.Errorf("context deadline exceeded")

	logs, win, err := readLogWindowCapped(&failingReader{data: lines + "2026-10-09T23:00:50Z hal", err: deadline}, end, 0, hardLogByteCap)
	if err != nil {
		t.Fatalf("a read cut after 50 lines must return them, got %v", err)
	}
	if win.Interrupted != "context deadline exceeded" || win.Capped || win.Lines != 50 || strings.Count(logs, "\n") != 50 {
		t.Errorf("window = %+v, %d lines", win, strings.Count(logs, "\n"))
	}
	if strings.Contains(logs, "hal") {
		t.Error("the half line the stream died in must be dropped")
	}

	if _, _, err := readLogWindowCapped(&failingReader{err: deadline}, end, 0, hardLogByteCap); err == nil {
		t.Error("a read that got nothing is an error")
	}
}

func TestReadLogAll(t *testing.T) {
	stream := strings.Repeat("0123456789\n", 10) // 110 bytes

	logs, win, err := readLogAll(strings.NewReader(stream), 55) // 5 lines
	if err != nil || !win.Capped || logs != strings.Repeat("0123456789\n", 5) {
		t.Errorf("cap on a boundary: logs=%q win=%+v err=%v", logs, win, err)
	}
	logs, win, err = readLogAll(strings.NewReader(stream), 60) // 5 lines and a half
	if err != nil || !win.Capped || logs != strings.Repeat("0123456789\n", 5) {
		t.Errorf("cap mid-line drops the half line: logs=%q win=%+v err=%v", logs, win, err)
	}
	logs, win, err = readLogAll(strings.NewReader(stream), 110) // exactly the log
	if err != nil || win.Capped || logs != stream {
		t.Errorf("a log exactly the cap is whole: win=%+v err=%v", win, err)
	}
	logs, win, err = readLogAll(&failingReader{data: "a\nb\nhal", err: fmt.Errorf("stream reset")}, 1<<20)
	if err != nil || win.Interrupted != "stream reset" || logs != "a\nb\n" {
		t.Errorf("a cut stream returns its whole lines: logs=%q win=%+v err=%v", logs, win, err)
	}
	if _, _, err := readLogAll(&failingReader{err: fmt.Errorf("stream reset")}, 1<<20); err == nil {
		t.Error("a read that got nothing is an error")
	}
}

func TestSplitLogTimestamp(t *testing.T) {
	ts, msg, ok := SplitLogTimestamp("2026-10-10T01:06:01.500123456Z GET / -> 200")
	if !ok || msg != "GET / -> 200" || !ts.Equal(time.Date(2026, 10, 10, 1, 6, 1, 500123456, time.UTC)) {
		t.Errorf("got %v %q %v", ts, msg, ok)
	}
	if _, msg, ok := SplitLogTimestamp("[orders-api] 01:06:01 GET / -> 200"); ok || msg != "[orders-api] 01:06:01 GET / -> 200" {
		t.Errorf("a line without a kubelet stamp is returned whole: %q %v", msg, ok)
	}
}
