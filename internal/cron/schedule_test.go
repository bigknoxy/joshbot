package cron

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		spec    string
		want    time.Duration
		wantErr bool
	}{
		{spec: "30m", want: 30 * time.Minute},
		{spec: "2h", want: 2 * time.Hour},
		{spec: "90s", want: 90 * time.Second},
		{spec: "1h30m", want: 90 * time.Minute},
		// "d" is not a Go duration unit, but it is what people write.
		{spec: "1d", want: 24 * time.Hour},
		{spec: "2d", want: 48 * time.Hour},
		{spec: "0.5d", want: 12 * time.Hour},
		{spec: " 3d ", want: 72 * time.Hour},
		{spec: "", wantErr: true},
		{spec: "d", wantErr: true},
		{spec: "tomorrow", wantErr: true},
		{spec: "5", wantErr: true}, // unitless
	}
	for _, tc := range tests {
		got, err := ParseDuration(tc.spec)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseDuration(%q) = %v, want error", tc.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDuration(%q) error: %v", tc.spec, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseDuration(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}

func TestParseSchedule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		schedule string
		wantKind string
		wantDur  time.Duration
		wantErr  bool
	}{
		{schedule: "delay:30m", wantKind: KindDelay, wantDur: 30 * time.Minute},
		{schedule: "every:1h", wantKind: KindEvery, wantDur: time.Hour},
		{schedule: "delay:1d", wantKind: KindDelay, wantDur: 24 * time.Hour},
		{schedule: "30m", wantErr: true},          // missing kind
		{schedule: "weekly:1h", wantErr: true},    // unknown kind
		{schedule: "every:0s", wantErr: true},     // non-positive
		{schedule: "every:-5m", wantErr: true},    // negative
		{schedule: "every:banana", wantErr: true}, // unparseable
		{schedule: "", wantErr: true},
	}
	for _, tc := range tests {
		kind, d, err := ParseSchedule(tc.schedule)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseSchedule(%q) = (%q, %v), want error", tc.schedule, kind, d)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSchedule(%q) error: %v", tc.schedule, err)
			continue
		}
		if kind != tc.wantKind || d != tc.wantDur {
			t.Errorf("ParseSchedule(%q) = (%q, %v), want (%q, %v)", tc.schedule, kind, d, tc.wantKind, tc.wantDur)
		}
	}
}

// A job whose schedule cannot be parsed must be rejected at AddJob rather than
// silently accepted and never scheduled.
func TestAddJob_RejectsInvalidSchedule(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)

	if err := s.AddJob(Job{ID: "bad", Schedule: "0 9 * * *", Channel: "cli", Content: "x"}); err == nil {
		t.Error("AddJob accepted a 5-field cron expression, which the scheduler cannot run")
	}
	if len(s.ListJobs()) != 0 {
		t.Error("rejected job was still stored")
	}
}

func TestAddJob_RejectsEmptyID(t *testing.T) {
	t.Parallel()
	s, _ := newTestService(t)
	if err := s.AddJob(Job{Schedule: "every:1h", Channel: "cli", Content: "x"}); err == nil {
		t.Error("AddJob accepted an empty ID")
	}
}
