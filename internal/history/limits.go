package history

// Usage limits: a web agent's site saying the owner's account is out of
// messages until a reset time. The agent stops sending to the site until
// then and reports itself limited to the relay (R6). The reset time is the
// site's own when its text names one, else the 429's Retry-After, else
// DefaultLimitWait. A site moving the owner to a smaller model and
// answering on is not a limit.

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultLimitWait is how long a site counts as limited when it said it was
// out of messages without saying when it resets.
const DefaultLimitWait = 60 * time.Minute

// maxLimitWait caps a reset time read from a site: a weekly limit, inside
// the relay's 8-day bound.
const maxLimitWait = 7 * 24 * time.Hour

// limitRule is one way a site words its usage limit in the page's alert or
// status regions. match finds it; its groups, when they matched, are the
// reset time, read by reset.
type limitRule struct {
	match *regexp.Regexp
	reset func(groups []string, now time.Time) (time.Time, bool)
}

// clockPattern is a reset time as a site writes it: "2:00 PM", "2pm".
const clockPattern = `(\d{1,2}(?::\d{2})?\s*[ap]\.?\s*m\.?)`

// claudeLimitRules is claude.ai's documented wording (its help center):
// "5-hour limit reached - resets [time]" and "You've reached your weekly
// limit. It resets [day] at [time]." Either without its reset part still
// counts, for DefaultLimitWait.
var claudeLimitRules = []limitRule{
	{
		match: regexp.MustCompile(`(?is)\b5-hour limit reached\b(?:.*?\bresets\s+(?:at\s+)?` + clockPattern + `)?`),
		reset: func(g []string, now time.Time) (time.Time, bool) { return nextClock(g[1], now) },
	},
	{
		match: regexp.MustCompile(`(?is)\byou(?:'|\x{2019})ve reached your weekly limit\b(?:.*?\bresets\s+([a-z]+)\s+at\s+` + clockPattern + `)?`),
		reset: func(g []string, now time.Time) (time.Time, bool) { return nextDayClock(g[1], g[2], now) },
	},
}

// detectLimit reads a usage limit from the page text the extension
// reported for src: the reset time and true when one of the site's rules
// matches. A site without rules never matches.
func detectLimit(src Source, notice string, now time.Time) (time.Time, bool) {
	site := siteFor(src)
	if site == nil || strings.TrimSpace(notice) == "" {
		return time.Time{}, false
	}
	for _, rule := range site.limitText {
		g := rule.match.FindStringSubmatch(notice)
		if g == nil {
			continue
		}
		if rule.reset != nil {
			if until, ok := rule.reset(g, now); ok && until.After(now) && !until.After(now.Add(maxLimitWait)) {
				return until, true
			}
		}
		return now.Add(DefaultLimitWait), true
	}
	return time.Time{}, false
}

// parseClock reads "2:00 PM" as an hour and minute of the day.
func parseClock(s string) (hour, minute int, ok bool) {
	s = strings.ToLower(strings.Join(strings.Fields(strings.ReplaceAll(s, ".", "")), ""))
	pm := strings.HasSuffix(s, "pm")
	if !pm && !strings.HasSuffix(s, "am") {
		return 0, 0, false
	}
	hm := strings.TrimSuffix(strings.TrimSuffix(s, "pm"), "am")
	h, m, hasMin := strings.Cut(hm, ":")
	hour, err := strconv.Atoi(h)
	if err != nil || hour < 1 || hour > 12 {
		return 0, 0, false
	}
	if hasMin {
		if minute, err = strconv.Atoi(m); err != nil || minute < 0 || minute > 59 {
			return 0, 0, false
		}
	}
	hour %= 12
	if pm {
		hour += 12
	}
	return hour, minute, true
}

// nextClock is the next time of day clock names after now, in now's zone.
func nextClock(clock string, now time.Time) (time.Time, bool) {
	hour, minute, ok := parseClock(clock)
	if !ok {
		return time.Time{}, false
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t, true
}

// nextDayClock is the next time a weekday ("Monday", "Mon") and clock
// name after now, in now's zone.
func nextDayClock(day, clock string, now time.Time) (time.Time, bool) {
	hour, minute, ok := parseClock(clock)
	day = strings.ToLower(day)
	if !ok || len(day) < 3 {
		return time.Time{}, false
	}
	for wd := time.Sunday; wd <= time.Saturday; wd++ {
		if !strings.HasPrefix(strings.ToLower(wd.String()), day) {
			continue
		}
		days := (int(wd) - int(now.Weekday()) + 7) % 7
		t := time.Date(now.Year(), now.Month(), now.Day()+days, hour, minute, 0, 0, now.Location())
		if !t.After(now) {
			t = t.AddDate(0, 0, 7)
		}
		return t, true
	}
	return time.Time{}, false
}
