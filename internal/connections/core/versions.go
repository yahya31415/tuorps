package core

import (
	"slices"
	"time"
)

// Pinned platform API versions (docs/api-reference.md, "Pinned versions").
// Adapters send exactly these; change them here, with the reference.
const (
	GraphVersion    = "v26.0"  // Meta Graph API, incl. graph.instagram.com
	LinkedInVersion = "202610" // LinkedIn-Version header
)

// PinnedVersion is a platform API version Tuorps calls, and when the
// platform stops serving it (zero when none is announced).
type PinnedVersion struct {
	API     string
	Version string
	Sunset  time.Time
}

// PinnedVersions returns the versions in use. Graph v26.0 has no fixed
// sunset yet; Meta guarantees "not before" 2028-07-29, which is used so that
// alerts can only come early. The result is a fresh slice.
func PinnedVersions() []PinnedVersion {
	return []PinnedVersion{
		{API: "meta-graph", Version: GraphVersion, Sunset: time.Date(2028, time.July, 29, 0, 0, 0, 0, time.UTC)},
		{API: "linkedin", Version: LinkedInVersion, Sunset: time.Date(2027, time.October, 15, 0, 0, 0, 0, time.UTC)},
		{API: "youtube-data", Version: "v3"},
	}
}

// SunsetAlert says a pinned version sunsets within the alert window, or
// already has (DaysLeft < 0).
type SunsetAlert struct {
	API      string
	Version  string
	Sunset   time.Time
	DaysLeft int
}

// SunsetAlerts lists, in input order, the versions whose sunset is at most
// window after now. Versions without an announced sunset never alert.
func SunsetAlerts(vs []PinnedVersion, now time.Time, window time.Duration) []SunsetAlert {
	var out []SunsetAlert
	for v := range slices.Values(vs) {
		if v.Sunset.IsZero() {
			continue
		}
		if left := v.Sunset.Sub(now); left <= window {
			out = append(out, SunsetAlert{API: v.API, Version: v.Version, Sunset: v.Sunset, DaysLeft: int(left / (24 * time.Hour))})
		}
	}
	return out
}
