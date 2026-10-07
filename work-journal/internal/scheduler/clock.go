package scheduler

import (
	"strings"
	"time"

	"workjournal/internal/config"
)

const dayLayout = "2006-01-02"

// closeInstant is the close time on now's date, in now's location.
func closeInstant(now time.Time, c config.Config) time.Time {
	h, m := c.CloseClock()
	return time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
}

// latestRollupDay is the most recent rollup weekday (midnight) whose close
// time plus SettleDelay is not after now.
func latestRollupDay(now time.Time, c config.Config) time.Time {
	want := rollupWeekday(c)
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for d.Weekday() != want || now.Before(closeInstant(d, c).Add(SettleDelay)) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func rollupWeekday(c config.Config) time.Weekday {
	for wd := time.Sunday; wd <= time.Saturday; wd++ {
		if strings.EqualFold(wd.String(), c.RollupWeekday) {
			return wd
		}
	}
	return time.Friday
}

func hourKey(now time.Time) string { return now.Format("2006-01-02T15") }

func parseDay(day string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation(dayLayout, day, loc)
}
