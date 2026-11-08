// Package backup makes, lists, prunes and restores backups (ADR 0020):
// a database snapshot, the admin certificate and the content store blobs it
// references, copied incrementally to a local folder or an SMB share.
package backup

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Frequencies.
const (
	Disabled = "disabled"
	Hourly   = "hourly"
	Daily    = "daily"
	Weekly   = "weekly"
	Monthly  = "monthly"
)

// Schedule is backup.schedule, interpreted in loc (the server's local time).
type Schedule struct {
	Frequency  string   `json:"frequency"`
	Minute     int      `json:"minute"`
	Time       string   `json:"time"`       // HH:MM
	DaysOfWeek []string `json:"daysOfWeek"` // mon..sun
	DayOfMonth int      `json:"dayOfMonth"` // 1..31; shorter months use their last day
}

// Key identifies a schedule's shape: a change means "start counting slots
// afresh" (no catch-up run for slots before the change).
func (s Schedule) Key() string {
	return fmt.Sprintf("%s|%d|%s|%s|%d", s.Frequency, s.Minute, s.Time, strings.Join(s.DaysOfWeek, ","), s.DayOfMonth)
}

var weekdays = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

// Validate reports what's wrong with a schedule, or "".
func (s Schedule) Validate() (field, msg string) {
	switch s.Frequency {
	case Disabled:
		return "", ""
	case Hourly:
		if s.Minute < 0 || s.Minute > 59 {
			return "minute", "must be 0 to 59"
		}
		return "", ""
	case Daily, Weekly, Monthly:
	default:
		return "frequency", "must be disabled, hourly, daily, weekly or monthly"
	}
	if _, _, ok := s.hm(); !ok {
		return "time", "must be HH:MM (24-hour)"
	}
	if s.Frequency == Weekly {
		if len(s.DaysOfWeek) == 0 {
			return "daysOfWeek", "needs at least one day"
		}
		for _, d := range s.DaysOfWeek {
			if _, ok := weekdays[d]; !ok {
				return "daysOfWeek", "days are mon, tue, wed, thu, fri, sat, sun"
			}
		}
	}
	if s.Frequency == Monthly && (s.DayOfMonth < 1 || s.DayOfMonth > 31) {
		return "dayOfMonth", "must be 1 to 31"
	}
	return "", ""
}

func (s Schedule) hm() (h, m int, ok bool) {
	a, b, found := strings.Cut(s.Time, ":")
	h, err1 := strconv.Atoi(a)
	m, err2 := strconv.Atoi(b)
	return h, m, found && err1 == nil && err2 == nil && len(a) == 2 && len(b) == 2 && h >= 0 && h < 24 && m >= 0 && m < 60
}

// slotOn is the scheduled moment on the given local day (y, mo, d), or
// false if that day has none. time.Date moves a time skipped by a clock
// change forward (the next valid moment) and picks one instance of a
// repeated time, so each slot runs once.
func (s Schedule) slotOn(y int, mo time.Month, d int, loc *time.Location) (time.Time, bool) {
	h, m, _ := s.hm()
	day := time.Date(y, mo, d, 12, 0, 0, 0, loc)
	switch s.Frequency {
	case Daily:
	case Weekly:
		ok := false
		for _, w := range s.DaysOfWeek {
			if weekdays[w] == day.Weekday() {
				ok = true
			}
		}
		if !ok {
			return time.Time{}, false
		}
	case Monthly:
		last := time.Date(y, mo+1, 0, 12, 0, 0, 0, loc).Day()
		if d != min(s.DayOfMonth, last) {
			return time.Time{}, false
		}
	default:
		return time.Time{}, false
	}
	return time.Date(y, mo, d, h, m, 0, 0, loc), true
}

// Next is the first slot strictly after t; false when disabled.
func (s Schedule) Next(t time.Time, loc *time.Location) (time.Time, bool) {
	if f, _ := s.Validate(); f != "" || s.Frequency == Disabled || s.Frequency == "" {
		return time.Time{}, false
	}
	t = t.In(loc)
	if s.Frequency == Hourly {
		c := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), s.Minute, 0, 0, loc)
		for !c.After(t) {
			n := c.Add(time.Hour)
			c = time.Date(n.Year(), n.Month(), n.Day(), n.Hour(), s.Minute, 0, 0, loc)
		}
		return c, true
	}
	for i := 0; i <= 62; i++ { // a monthly slot is at most ~62 days away
		d := time.Date(t.Year(), t.Month(), t.Day()+i, 12, 0, 0, 0, loc)
		if c, ok := s.slotOn(d.Year(), d.Month(), d.Day(), loc); ok && c.After(t) {
			return c, true
		}
	}
	return time.Time{}, false
}

// Prev is the last slot at or before t; false when disabled.
func (s Schedule) Prev(t time.Time, loc *time.Location) (time.Time, bool) {
	if f, _ := s.Validate(); f != "" || s.Frequency == Disabled || s.Frequency == "" {
		return time.Time{}, false
	}
	t = t.In(loc)
	if s.Frequency == Hourly {
		c := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), s.Minute, 0, 0, loc)
		for c.After(t) {
			p := c.Add(-time.Hour)
			c = time.Date(p.Year(), p.Month(), p.Day(), p.Hour(), s.Minute, 0, 0, loc)
		}
		return c, true
	}
	for i := 0; i <= 62; i++ {
		d := time.Date(t.Year(), t.Month(), t.Day()-i, 12, 0, 0, 0, loc)
		if c, ok := s.slotOn(d.Year(), d.Month(), d.Day(), loc); ok && !c.After(t) {
			return c, true
		}
	}
	return time.Time{}, false
}
