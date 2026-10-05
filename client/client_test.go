package client_test

import (
	"testing"
	"time"

	"github.com/draftplane/draftplane/client"
)

// TestAgo pins the relative-time formatter across every branch, with the clock
// passed in so the boundary cases are exact.
//
// The negative row is the one that matters: a clock stepped back after at was
// recorded must not render "-1m ago". The zero row is the other structural
// case -- "" lets a door append this unconditionally.
func TestAgo(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"no timestamp at all", time.Time{}, ""},
		{"at is ahead of now", now.Add(30 * time.Second), "moments ago"},
		{"this instant", now, "moments ago"},
		{"under a minute", now.Add(-59 * time.Second), "moments ago"},
		{"exactly a minute", now.Add(-time.Minute), "1m ago"},
		{"minutes, floored", now.Add(-119 * time.Second), "1m ago"},
		{"under an hour", now.Add(-59 * time.Minute), "59m ago"},
		{"exactly an hour", now.Add(-time.Hour), "1h ago"},
		{"two hours ago", now.Add(-2 * time.Hour), "2h ago"},
		{"hours, floored", now.Add(-23*time.Hour - 59*time.Minute), "23h ago"},
		{"exactly a day", now.Add(-24 * time.Hour), "1d ago"},
		{"days, floored rather than rounded up to hours", now.Add(-92 * time.Hour), "3d ago"},
		{"a year and more", now.Add(-400 * 24 * time.Hour), "400d ago"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := client.Ago(tc.at, now); got != tc.want {
				t.Errorf("Ago = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestURLSource enumerates which sources URLSource counts as a URL.
func TestURLSource(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{name: "bare absolute local path", source: "/plans/one.md", want: false},
		{name: "file URL is still local", source: "file:///plans/one.md", want: false},
		{name: "sourceless plan", source: "", want: false},
		{name: "https URL is a URL source", source: "https://example.com/plan.md", want: true},
		{name: "notion URL is a URL source", source: "notion://workspace/page", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := client.URLSource(tt.source); got != tt.want {
				t.Errorf("URLSource(%q) = %v, want %v", tt.source, got, tt.want)
			}
		})
	}
}
