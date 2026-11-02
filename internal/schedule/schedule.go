// Package schedule parses the cron expressions of scan roots. The scanner
// evaluates them and the admin API validates them, so both share one parser.
package schedule

import (
	"strings"
	_ "time/tzdata" // CRON_TZ=<zone> must work in a distroless image

	"github.com/robfig/cron/v3"
)

// Parse parses a standard 5-field cron expression (or @daily and the like).
// ParseStandard evaluates a spec without a time-zone prefix in time.Local, but
// schedules are UTC unless they say otherwise, so such a spec gets an explicit
// CRON_TZ=UTC prefix. The prefix is only a parse input; error messages keep
// the original text. spec must not be empty: callers treat "" as no schedule.
func Parse(spec string) (cron.Schedule, error) {
	if !strings.HasPrefix(spec, "TZ=") && !strings.HasPrefix(spec, "CRON_TZ=") {
		sched, err := cron.ParseStandard("CRON_TZ=UTC " + spec)
		if err != nil {
			// Re-parse the original so the message does not mention the prefix.
			_, err = cron.ParseStandard(spec)
		}
		return sched, err
	}
	return cron.ParseStandard(spec)
}
