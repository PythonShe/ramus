package ui

import (
	"testing"
	"time"
)

func waitZones(t *testing.T) {
	t.Helper()
	loadZones()
	deadline := time.Now().Add(5 * time.Second)
	for zones.Load() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the zones were not loaded")
		}
		time.Sleep(time.Millisecond)
	}
	if len(*zones.Load()) == 0 {
		t.Skip("this system has no zone database")
	}
}

func TestUTCOffset(t *testing.T) {
	for off, want := range map[int]string{0: "", 8 * 3600: "+8", -3 * 3600: "-3", 5*3600 + 1800: "+5:30", -(3*3600 + 1800): "-3:30", 5*3600 + 45*60: "+5:45"} {
		if got := utcOffset(off); got != want {
			t.Errorf("utcOffset(%d) = %q, want %q", off, got, want)
		}
	}
}

func TestZoneTimeNamesPlacesAtTheOffsetThen(t *testing.T) {
	waitZones(t)
	at := func(s string, off int) time.Time {
		w, err := time.Parse("2006-01-02 15:04:05", s)
		if err != nil {
			t.Fatal(err)
		}
		return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), 0, time.FixedZone("", off))
	}
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{at("2026-11-17 21:07:44", 8*3600), "2026-11-17 21:07:44 UTC+8 (Shanghai, Hong Kong, Singapore)"},
		// Daylight saving moves London to +1 in summer and Berlin to +2.
		{at("2026-07-01 12:00:00", 3600), "2026-07-01 12:00:00 UTC+1 (London, Lisbon, Lagos)"},
		{at("2026-01-15 12:00:00", 3600), "2026-01-15 12:00:00 UTC+1 (Berlin, Paris, Lagos)"},
		{at("2026-01-15 12:00:00", 5*3600+1800), "2026-01-15 12:00:00 UTC+5:30 (Bengaluru, Colombo)"},
		{at("2026-01-15 12:00:00", 5*3600+45*60), "2026-01-15 12:00:00 UTC+5:45 (Kathmandu)"},
		// An offset no place keeps.
		{at("2026-01-15 12:00:00", 13*3600+60), "2026-01-15 12:00:00 UTC+13:01"},
	} {
		if got := zoneTime(c.t); got != c.want {
			t.Errorf("zoneTime(%v) = %q, want %q", c.t, got, c.want)
		}
	}
}
