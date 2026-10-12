package agent

import (
	"testing"
	"time"
)

func TestWriteFailureLog_OneLineAMinute(t *testing.T) {
	var l writeFailureLog
	t0 := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)

	if logNow, failed := l.record(t0); !logNow || failed != 1 {
		t.Fatalf("first failure = (%v, %d), want it logged with 1", logNow, failed)
	}
	for i := 1; i <= 5; i++ {
		if logNow, _ := l.record(t0.Add(time.Duration(i) * 10 * time.Second)); logNow {
			t.Fatalf("failure %d inside the minute was logged", i)
		}
	}
	// The five held back plus this one.
	if logNow, failed := l.record(t0.Add(time.Minute)); !logNow || failed != 6 {
		t.Fatalf("after a minute = (%v, %d), want it logged with 6", logNow, failed)
	}
}
