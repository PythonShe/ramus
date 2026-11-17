package ui

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"ramus/locales"
)

// zonePlaces are the time zones that a commit's UTC offset is matched
// against to suggest where its author may be, most likely first among
// those sharing an offset. A commit records its offset only, never its
// zone, so these are guesses; a zone is matched by its rules at the
// commit's time, so that daylight saving moves a place between offsets.
// key names the place in the catalog's timeZonePlace.
var zonePlaces = []struct{ zone, key string }{
	{"America/Los_Angeles", "losangeles"},
	{"America/New_York", "newyork"},
	{"America/Chicago", "chicago"},
	{"America/Denver", "denver"},
	{"America/Phoenix", "phoenix"},
	{"America/Anchorage", "anchorage"},
	{"Pacific/Honolulu", "honolulu"},
	{"America/Mexico_City", "mexicocity"},
	{"America/Bogota", "bogota"},
	{"America/Sao_Paulo", "saopaulo"},
	{"America/Argentina/Buenos_Aires", "buenosaires"},
	{"America/Santiago", "santiago"},
	{"America/St_Johns", "stjohns"},
	{"Atlantic/Azores", "azores"},
	{"Europe/London", "london"},
	{"Europe/Lisbon", "lisbon"},
	{"Atlantic/Reykjavik", "reykjavik"},
	{"Europe/Berlin", "berlin"},
	{"Europe/Paris", "paris"},
	{"Africa/Lagos", "lagos"},
	{"Europe/Athens", "athens"},
	{"Asia/Jerusalem", "telaviv"},
	{"Africa/Cairo", "cairo"},
	{"Africa/Johannesburg", "johannesburg"},
	{"Europe/Moscow", "moscow"},
	{"Europe/Istanbul", "istanbul"},
	{"Asia/Riyadh", "riyadh"},
	{"Africa/Nairobi", "nairobi"},
	{"Asia/Tehran", "tehran"},
	{"Asia/Dubai", "dubai"},
	{"Asia/Kabul", "kabul"},
	{"Asia/Karachi", "karachi"},
	{"Asia/Tashkent", "tashkent"},
	{"Asia/Kolkata", "bengaluru"},
	{"Asia/Colombo", "colombo"},
	{"Asia/Kathmandu", "kathmandu"},
	{"Asia/Dhaka", "dhaka"},
	{"Asia/Yangon", "yangon"},
	{"Asia/Bangkok", "bangkok"},
	{"Asia/Jakarta", "jakarta"},
	{"Asia/Ho_Chi_Minh", "hochiminh"},
	{"Asia/Shanghai", "shanghai"},
	{"Asia/Hong_Kong", "hongkong"},
	{"Asia/Singapore", "singapore"},
	{"Asia/Taipei", "taipei"},
	{"Australia/Perth", "perth"},
	{"Asia/Tokyo", "tokyo"},
	{"Asia/Seoul", "seoul"},
	{"Australia/Adelaide", "adelaide"},
	{"Australia/Sydney", "sydney"},
	{"Australia/Brisbane", "brisbane"},
	{"Pacific/Auckland", "auckland"},
}

// maxZonePlaces is how many places a time names at most.
const maxZonePlaces = 3

type loadedZone struct {
	loc *time.Location
	key string
}

// zones holds zonePlaces' locations once loadZones has read them; nil
// until then, when times name no places.
var (
	zones     atomic.Pointer[[]loadedZone]
	zonesOnce sync.Once
)

// loadZones reads zonePlaces' rules in the background, as they are read
// from disk; a zone the system lacks is left out.
func loadZones() {
	zonesOnce.Do(func() {
		go func() {
			out := make([]loadedZone, 0, len(zonePlaces))
			for _, z := range zonePlaces {
				if loc, err := time.LoadLocation(z.zone); err == nil {
					out = append(out, loadedZone{loc, z.key})
				}
			}
			zones.Store(&out)
		}()
	})
}

// zoneTime says t, which is in the zone of the person who made a commit,
// with its UTC offset and the places that were at that offset then.
func zoneTime(t time.Time) string {
	_, off := t.Zone()
	at := t.Format("2006-01-02 15:04:05")
	o := utcOffset(off)
	var keys []string
	if zs := zones.Load(); zs != nil {
		for _, z := range *zs {
			if _, o := t.In(z.loc).Zone(); o == off {
				keys = append(keys, z.key)
				if len(keys) == maxZonePlaces {
					break
				}
			}
		}
	}
	place := func(i int) string { return locales.TimeZonePlace(keys[i]) }
	switch len(keys) {
	case 0:
		return locales.CommitTimeZone(at, o)
	case 1:
		return locales.CommitTimeZoneIn1(at, o, place(0))
	case 2:
		return locales.CommitTimeZoneIn2(at, o, place(0), place(1))
	default:
		return locales.CommitTimeZoneIn3(at, o, place(0), place(1), place(2))
	}
}

// utcOffset writes an offset in seconds as it follows "UTC": "+8",
// "-3:30", and nothing for UTC itself.
func utcOffset(off int) string {
	if off == 0 {
		return ""
	}
	sign := '+'
	if off < 0 {
		sign, off = '-', -off
	}
	h, m := off/3600, off%3600/60
	if m == 0 {
		return fmt.Sprintf("%c%d", sign, h)
	}
	return fmt.Sprintf("%c%d:%02d", sign, h, m)
}
