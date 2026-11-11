package coder

import (
	"testing"
	"time"
)

func TestExtension(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	at := func(h float64) time.Time { return now.Add(time.Duration(h * float64(time.Hour))) }
	for _, c := range []struct {
		name          string
		deadline, max time.Time
		want          time.Duration
		capped        bool
	}{
		{"autostop なし", time.Time{}, time.Time{}, 0, false},
		{"他の人がもっと先まで延ばしている", at(10), time.Time{}, 0, false},
		{"足りないので延ばす", at(1), time.Time{}, 8 * time.Hour, false},
		{"上限で切る", at(1), at(5.5), 5*time.Hour + 30*time.Minute, true},
		{"上限が今の予定と同じ", at(3), at(3), 0, true},
		{"延ばす幅が 30 分未満", at(0.1), at(0.2), 30 * time.Minute, true},
	} {
		var w Workspace
		w.LatestBuild.Deadline, w.LatestBuild.MaxDeadline = c.deadline, c.max
		if d, capped := w.Extension(now, 8*time.Hour); d != c.want || capped != c.capped {
			t.Errorf("%s: got %v %v, want %v %v", c.name, d, capped, c.want, c.capped)
		}
	}
}
