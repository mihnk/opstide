// Package digest is the morning summary: what the record says about a stretch of time (by default the night before),
// as data, and as the lines a person reads in a chat channel, in English or Turkish. It holds no logic about the
// cluster: internal/api fills a Morning from the record, internal/notify sends its lines, and the UI shows the same
// lines as a preview, so what people are shown in setup is what their channel gets.
package digest

import "time"

// Schema is the version of Morning as a webhook receives it.
const Schema = 1

// What a summary may include (notify.morningSummary.include).
const (
	IncludeFailures = "failures" // failed runs and CronJobs overdue for a success
	IncludeTroubles = "troubles" // restarts, OOMKills, evictions and troubled releases, with what changed before them
	IncludeGaps     = "gaps"     // watches that were not whole
	IncludeDue      = "due"      // what is still to run today
	IncludeChanges  = "changes"  // every change recorded, not only the count
)

// AllIncludes lists what a summary may include; DefaultIncludes is what it does unless configured.
var (
	AllIncludes     = []string{IncludeFailures, IncludeTroubles, IncludeGaps, IncludeDue, IncludeChanges}
	DefaultIncludes = []string{IncludeFailures, IncludeTroubles, IncludeGaps, IncludeDue}
)

// How far back a summary looks (notify.morningSummary.covers).
const (
	CoversSince18 = "since-18h" // from 18:00 the day before, in the summary's zone
	Covers12h     = "12h"
	Covers24h     = "24h"
)

// Limits on how much one message lists; the rest is counted.
const (
	MaxListed = 8
)

// Morning is what the record says about [From, To). Lists are cut at MaxListed; the *More fields count the rest.
type Morning struct {
	Schema   int       `json:"schema"`
	Cluster  string    `json:"cluster"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	TimeZone string    `json:"timeZone"`
	Language string    `json:"language"`
	Include  []string  `json:"include"`
	Test     bool      `json:"test,omitempty"` // a test message, sent by hand from Integrations
	Late     bool      `json:"late,omitempty"` // sent after its time, because OpsTide was not running then
	Demo     bool      `json:"demo,omitempty"`

	Runs        Runs      `json:"runs"`
	Failed      []Run     `json:"failed"`
	FailedMore  int       `json:"failedMore,omitempty"`
	Overdue     []Overdue `json:"overdue"`
	OverdueMore int       `json:"overdueMore,omitempty"`
	Troubles    []Trouble `json:"troubles"`
	TroubleMore int       `json:"troublesMore,omitempty"`
	Changes     int       `json:"changes"`
	ChangeList  []Change  `json:"changeList,omitempty"`
	ChangesMore int       `json:"changesMore,omitempty"`
	Warnings    int       `json:"warnings"`
	// NotWatched names the watches that were not whole in the stretch: nothing is said about them then.
	NotWatched []string `json:"notWatched"`
	Due        Due      `json:"due"`
	Link       string   `json:"link,omitempty"` // the Overview, when OpsTide knows its public address
}

// Runs counts the CronJob runs that started or ended in the stretch.
type Runs struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Unknown   int `json:"unknown"`
	Running   int `json:"running"`
}

// Run is a failed run.
type Run struct {
	Name        string    `json:"name"` // the CronJob's display name, or its name
	Namespace   string    `json:"namespace"`
	CronJob     string    `json:"cronJob"`
	At          time.Time `json:"at"`
	Maintenance string    `json:"maintenance,omitempty"` // the maintenance window it fell in
	Link        string    `json:"link,omitempty"`
}

// Overdue is a CronJob that went longer than its must-succeed-within without a success, while OpsTide watched.
type Overdue struct {
	Name          string    `json:"name"`
	Namespace     string    `json:"namespace"`
	CronJob       string    `json:"cronJob"`
	WithinSeconds int64     `json:"withinSeconds"`
	LastSuccess   time.Time `json:"lastSuccess,omitzero"`
	Link          string    `json:"link,omitempty"`
}

// Trouble is a record that marks something going wrong in a workload, with the change recorded nearest before it.
type Trouble struct {
	Type    string    `json:"type"`
	Summary string    `json:"summary"`
	At      time.Time `json:"at"`
	Link    string    `json:"link,omitempty"`
	// ChangesBefore counts the changes recorded in the hour before, on it or what it is connected to; Nearest is the
	// closest. Earlier is not a cause.
	ChangesBefore int     `json:"changesBefore"`
	Nearest       *Change `json:"nearest,omitempty"`
}

// Change is one recorded change.
type Change struct {
	Summary       string    `json:"summary"`
	At            time.Time `json:"at"`
	Writer        string    `json:"writer,omitempty"`
	SecondsBefore int64     `json:"secondsBefore,omitempty"` // for Trouble.Nearest
	Link          string    `json:"link,omitempty"`
}

// Due is what is still expected to run today, after the summary's time.
type Due struct {
	Count    int       `json:"count"`
	NextName string    `json:"nextName,omitempty"`
	NextAt   time.Time `json:"nextAt,omitzero"`
}

// NeedsLook says whether anything in the summary asks for a person: a failure, an overdue CronJob, a trouble, or a
// stretch not watched.
func (m Morning) NeedsLook() bool {
	return len(m.Failed) > 0 || len(m.Overdue) > 0 || len(m.Troubles) > 0 || len(m.NotWatched) > 0
}

// Includes says whether the summary includes this part.
func (m Morning) Includes(part string) bool {
	for _, p := range m.Include {
		if p == part {
			return true
		}
	}
	return false
}

// ValidCovers says whether covers is one of the stretches a summary may look back over.
func ValidCovers(covers string) bool {
	return covers == CoversSince18 || covers == Covers12h || covers == Covers24h
}

// Since is where a summary sent at `at` starts looking: 18:00 the day before (in loc), or 12 or 24 hours back.
func Since(covers string, at time.Time, loc *time.Location) time.Time {
	switch covers {
	case Covers12h:
		return at.Add(-12 * time.Hour)
	case Covers24h:
		return at.Add(-24 * time.Hour)
	default:
		y, m, d := at.In(loc).AddDate(0, 0, -1).Date()
		return time.Date(y, m, d, 18, 0, 0, 0, loc).UTC()
	}
}
