package sla

import (
	"testing"
	"time"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return parsed.UTC()
}

func TestAddTargetElapsed(t *testing.T) {
	// Without a business calendar the target is plain elapsed time, unchanged
	// from the previous behaviour.
	start := at(t, "2026-08-07T17:00:00Z") // Friday 17:00
	got := addTarget(start, 240, false)
	want := at(t, "2026-08-07T21:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("elapsed: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessSameDay(t *testing.T) {
	start := at(t, "2026-08-10T10:00:00Z") // Monday 10:00
	got := addTarget(start, 120, true)
	want := at(t, "2026-08-10T12:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("same day: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessOvernight(t *testing.T) {
	// 4 business hours from Friday 17:00: 1h on Friday + 3h on Monday → Mon 11:00.
	start := at(t, "2026-08-07T17:00:00Z")
	got := addTarget(start, 240, true)
	want := at(t, "2026-08-10T11:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("overnight: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessWeekend(t *testing.T) {
	// Created on Saturday: the clock starts Monday 08:00; 60 minutes → Mon 09:00.
	start := at(t, "2026-08-08T12:00:00Z") // Saturday
	got := addTarget(start, 60, true)
	want := at(t, "2026-08-10T09:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("weekend: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessBeforeHours(t *testing.T) {
	// Created before business hours: the clock starts at 08:00 the same day.
	start := at(t, "2026-08-11T06:30:00Z") // Tuesday 06:30
	got := addTarget(start, 90, true)
	want := at(t, "2026-08-11T09:30:00Z")
	if !got.Equal(want) {
		t.Fatalf("before hours: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessAfterHours(t *testing.T) {
	// Created after business hours: the clock starts the next weekday at 08:00.
	start := at(t, "2026-08-11T19:00:00Z") // Tuesday 19:00
	got := addTarget(start, 30, true)
	want := at(t, "2026-08-12T08:30:00Z")
	if !got.Equal(want) {
		t.Fatalf("after hours: got %s, want %s", got, want)
	}
}

func TestAddTargetBusinessMultiDay(t *testing.T) {
	// A full business day has 10 hours (08:00–18:00); 12 business hours from
	// Monday 08:00 therefore end on Tuesday at 10:00.
	start := at(t, "2026-08-10T08:00:00Z")
	got := addTarget(start, 720, true)
	want := at(t, "2026-08-11T10:00:00Z")
	if !got.Equal(want) {
		t.Fatalf("multi day: got %s, want %s", got, want)
	}
}

func TestAddTargetZeroMinutes(t *testing.T) {
	start := at(t, "2026-08-10T10:00:00Z")
	if got := addTarget(start, 0, true); !got.Equal(start) {
		t.Fatalf("zero minutes should return start unchanged, got %s", got)
	}
}
