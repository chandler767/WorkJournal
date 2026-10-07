package scheduler

import (
	"strings"
	"time"

	"workjournal/internal/config"
)

const dayLayout = "2006-01-02"

// closeInstant is today's close time in now's location.
func closeInstant(now time.Time, c config.Config) time.Time {
	h, m := c.CloseClock()
	return time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
}

func isRollupDay(now time.Time, c config.Config) bool {
	return strings.EqualFold(now.Weekday().String(), c.RollupWeekday)
}

func hourKey(now time.Time) string { return now.Format("2006-01-02T15") }

func parseDay(day string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(dayLayout, day, loc)
}
