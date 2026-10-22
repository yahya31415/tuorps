package core_test

import (
	"slices"
	"testing"
	"time"

	"github.com/yahya31415/tuorps/internal/connections/core"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// The pinned versions and sunsets come from docs/api-reference.md
// ("Pinned versions"); adapters use the same constants.
func TestPinnedVersions(t *testing.T) {
	vs := core.PinnedVersions()
	find := func(api string) core.PinnedVersion {
		i := slices.IndexFunc(vs, func(v core.PinnedVersion) bool { return v.API == api })
		if i < 0 {
			t.Fatalf("no pinned version for %s", api)
		}
		return vs[i]
	}
	if core.GraphVersion != "v26.0" || core.LinkedInVersion != "202610" {
		t.Fatalf("pinned constants = %s, %s; update docs/api-reference.md with them", core.GraphVersion, core.LinkedInVersion)
	}
	if g := find("meta-graph"); g.Version != core.GraphVersion || !g.Sunset.Equal(day(2028, time.July, 29)) {
		t.Errorf("Graph = %+v", g)
	}
	if l := find("linkedin"); l.Version != core.LinkedInVersion || !l.Sunset.Equal(day(2027, time.October, 15)) {
		t.Errorf("LinkedIn = %+v", l)
	}
	if y := find("youtube-data"); y.Version != "v3" || !y.Sunset.IsZero() {
		t.Errorf("YouTube = %+v, want v3 with no announced sunset", y)
	}
	vs[0].Version = "mutated"
	if core.PinnedVersions()[0].Version == "mutated" {
		t.Error("PinnedVersions shares its slice with callers")
	}
}

func TestSunsetAlerts(t *testing.T) {
	vs := []core.PinnedVersion{
		{API: "linkedin", Version: "202610", Sunset: day(2027, time.October, 15)},
		{API: "youtube-data", Version: "v3"},
		{API: "old", Version: "1", Sunset: day(2027, time.January, 1)},
	}
	const window = 60 * 24 * time.Hour
	cases := []struct {
		now  time.Time
		want []core.SunsetAlert
	}{
		{day(2026, time.October, 22), nil},
		{day(2026, time.November, 2), []core.SunsetAlert{{API: "old", Version: "1", Sunset: day(2027, time.January, 1), DaysLeft: 60}}},
		{day(2027, time.August, 16), []core.SunsetAlert{
			{API: "linkedin", Version: "202610", Sunset: day(2027, time.October, 15), DaysLeft: 60},
			{API: "old", Version: "1", Sunset: day(2027, time.January, 1), DaysLeft: -227},
		}},
		{day(2027, time.August, 15), []core.SunsetAlert{
			{API: "old", Version: "1", Sunset: day(2027, time.January, 1), DaysLeft: -226},
		}},
	}
	for _, tc := range cases {
		got := core.SunsetAlerts(vs, tc.now, window)
		if !slices.Equal(got, tc.want) {
			t.Errorf("SunsetAlerts(%s) = %+v, want %+v", tc.now.Format(time.DateOnly), got, tc.want)
		}
	}
	if got := core.SunsetAlerts(nil, day(2027, time.January, 1), window); got != nil {
		t.Errorf("no versions gave %+v", got)
	}
}
