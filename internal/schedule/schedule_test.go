package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for _, ok := range []string{"@daily", "0 3 * * *", "CRON_TZ=Europe/Rome 0 3 * * *", "TZ=UTC 0 * * * *"} {
		if _, err := Parse(ok); err != nil {
			t.Errorf("Parse(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"61 * * * *", "bogus", "TZ=Nowhere/X 0 * * * *", "* * *"} {
		_, err := Parse(bad)
		if err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
			continue
		}
		if bad == "bogus" && strings.Contains(err.Error(), "CRON_TZ") {
			t.Errorf("error for %q mentions the prefix: %v", bad, err)
		}
	}
}

// A spec without a zone is UTC whatever zone Next is given.
func TestParse_UTCForAnyInputZone(t *testing.T) {
	plus5 := time.FixedZone("plus5", 5*3600)
	sched, err := Parse("0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	got := sched.Next(time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC).In(plus5))
	if want := time.Date(2026, 1, 11, 3, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got.UTC(), want)
	}
}
