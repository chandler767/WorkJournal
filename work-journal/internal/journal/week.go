package journal

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"workjournal/internal/config"
)

var weekRE = regexp.MustCompile(`^(\d{4})-W(\d{2})$`)

// WeekDays returns the seven YYYY-MM-DD days (Monday to Sunday) of an ISO
// week key such as 2026-W41. Keys that are not a real ISO week (W00, or W53
// in a 52-week year) are rejected.
func WeekDays(week string) ([]string, error) {
	m := weekRE.FindStringSubmatch(week)
	if m == nil {
		return nil, fmt.Errorf("journal: bad week %q", week)
	}
	year, _ := strconv.Atoi(m[1])
	n, _ := strconv.Atoi(m[2])
	// January 4th is always in ISO week 1; step back to that week's Monday.
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday())+6)%7)).AddDate(0, 0, (n-1)*7)
	if config.WeekKey(monday) != week {
		return nil, fmt.Errorf("journal: %q is not an ISO week", week)
	}
	days := make([]string, 7)
	for i := range days {
		days[i] = monday.AddDate(0, 0, i).Format("2006-01-02")
	}
	return days, nil
}
