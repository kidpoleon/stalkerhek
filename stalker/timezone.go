package stalker

import (
	"strings"
	"time"
)

// TimeLocation returns the portal's configured IANA timezone, or UTC.
func (p *Portal) TimeLocation() *time.Location {
	if p == nil {
		return time.UTC
	}
	tz := strings.TrimSpace(p.TimeZone)
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC
	}
	return loc
}

func epochInZone(ts int64, loc *time.Location) time.Time {
	if ts > 10_000_000_000 {
		ts = ts / 1000
	}
	if ts <= 0 {
		return time.Time{}
	}
	if loc == nil {
		loc = time.UTC
	}
	return time.Unix(ts, 0).In(loc)
}
