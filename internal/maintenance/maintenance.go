// Package maintenance reads the maintenance windows an operator declares in configuration (Helm values, rendered into
// a file the binary reads at start) and answers which window, if any, covers a time in a namespace.
//
// A window is context, never a filter: records inside one are still recorded, listed and counted. OpsTide only adds
// "inside maintenance window <name>" to them, as a DERIVED statement (a rule applied to the configured windows), so
// planned work can be told from an incident. Windows are never edited from the UI or the API.
package maintenance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/robfig/cron/v3"
	"sigs.k8s.io/yaml"

	"github.com/mihnk/opstide/internal/schedule"
)

// Limits on what a configuration may declare, so a mistake cannot make every request expand thousands of times.
const (
	MaxWindows    = 100
	MaxNamespaces = 50
	// MaxOccurrences bounds how many occurrences of one recurring window a single question expands.
	MaxOccurrences = 1000
	// MaxExpansion bounds how many occurrences of all windows together a single question expands (the per-request
	// budget): past it the answer says it was truncated, and a time is looked up window by window instead.
	MaxExpansion = 10_000
	MaxOneOff    = 31 * 24 * time.Hour // the longest one-off window
	MaxDuration  = 7 * 24 * time.Hour  // the longest occurrence of a recurring window
	MinDuration  = time.Minute
	maxFileBytes = 1 << 20
	maxNote      = 500
)

// Spec is one window as written in the configuration file. Exactly one of {start, end} and {cron, duration} is set.
type Spec struct {
	Name       string   `json:"name"`
	Start      string   `json:"start,omitempty"`    // RFC 3339, one-off windows
	End        string   `json:"end,omitempty"`      // RFC 3339, one-off windows
	Cron       string   `json:"cron,omitempty"`     // 5-field cron, recurring windows
	Duration   string   `json:"duration,omitempty"` // Go duration (90m, 2h), recurring windows
	TimeZone   string   `json:"timeZone,omitempty"` // IANA zone the cron is read in; UTC when empty
	Namespaces []string `json:"namespaces,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// File is the configuration file: {windows: [...]}.
type File struct {
	Windows []Spec `json:"windows"`
}

// Window is a validated window.
type Window struct {
	Name       string
	Note       string
	Namespaces []string // empty: the whole cluster, cluster-scoped records included
	// One-off windows.
	Start, End time.Time
	// Recurring windows.
	Cron     string
	TimeZone string
	Duration time.Duration
	spec     cron.Schedule // read on the wall clock of loc (see next)
	loc      *time.Location
}

// Recurring says whether the window repeats on a cron schedule.
func (w Window) Recurring() bool { return w.spec != nil }

// Covers says whether the window applies to a record in this namespace ("" for a cluster-scoped record, which only a
// cluster-wide window covers).
func (w Window) Covers(ns string) bool { return covers(w.Namespaces, ns) }

func covers(list []string, ns string) bool {
	if len(list) == 0 {
		return true
	}
	for _, n := range list {
		if n == ns {
			return true
		}
	}
	return false
}

// Occurrence is one stretch of time a window is open.
type Occurrence struct {
	Name       string
	Note       string
	Namespaces []string
	From, To   time.Time // [From, To)
	Recurring  bool
}

// Covers says whether the occurrence applies to a record in this namespace at t.
func (o Occurrence) Covers(t time.Time, ns string) bool {
	return !t.Before(o.From) && t.Before(o.To) && covers(o.Namespaces, ns)
}

// Set is the configured windows. The zero value and nil both mean none.
type Set struct {
	windows []Window
}

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._:/()-]{0,78}[A-Za-z0-9)]$|^[A-Za-z0-9]$`)
	nsRe   = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
)

// LoadFile reads and validates a configuration file. An empty path means no windows.
func LoadFile(path string) (*Set, error) {
	if path == "" {
		return &Set{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("maintenance file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("maintenance file %s: %w", path, err)
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("maintenance file %s is larger than 1 MiB", path)
	}
	s, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("maintenance file %s: %w", path, err)
	}
	return s, nil
}

// Parse reads YAML (or JSON) and validates every window. Unknown fields, a bad time or cron, or a window that is too
// long or too many are errors: OpsTide refuses to start rather than guess what was meant.
func Parse(b []byte) (*Set, error) {
	var f File
	if err := yaml.UnmarshalStrict(b, &f); err != nil {
		return nil, fmt.Errorf("cannot read it: %w", err)
	}
	return New(f.Windows)
}

// New validates specs into a Set.
func New(specs []Spec) (*Set, error) {
	if len(specs) > MaxWindows {
		return nil, fmt.Errorf("%d windows; at most %d are allowed", len(specs), MaxWindows)
	}
	s := &Set{}
	seen := map[string]bool{}
	for i, sp := range specs {
		w, err := validate(sp)
		if err != nil {
			name := sp.Name
			if name == "" {
				name = fmt.Sprintf("#%d", i+1)
			}
			return nil, fmt.Errorf("window %q: %w", name, err)
		}
		if seen[w.Name] {
			return nil, fmt.Errorf("window %q is declared twice", w.Name)
		}
		seen[w.Name] = true
		s.windows = append(s.windows, w)
	}
	return s, nil
}

func validate(sp Spec) (Window, error) {
	w := Window{Name: sp.Name, Note: strings.TrimSpace(sp.Note)}
	if !nameRe.MatchString(sp.Name) {
		return w, errors.New("name must be 1 to 80 letters, digits, spaces or . _ : / ( ) -, starting and ending with a letter or digit")
	}
	if len(w.Note) > maxNote {
		return w, fmt.Errorf("note is longer than %d characters", maxNote)
	}
	for _, r := range w.Note {
		if unicode.IsControl(r) && r != '\n' {
			return w, errors.New("note holds a control character")
		}
	}
	if len(sp.Namespaces) > MaxNamespaces {
		return w, fmt.Errorf("more than %d namespaces", MaxNamespaces)
	}
	nsSeen := map[string]bool{}
	for _, ns := range sp.Namespaces {
		if !nsRe.MatchString(ns) {
			return w, fmt.Errorf("namespace %q is not a valid namespace name", ns)
		}
		if !nsSeen[ns] {
			nsSeen[ns] = true
			w.Namespaces = append(w.Namespaces, ns)
		}
	}
	sort.Strings(w.Namespaces)
	oneOff := sp.Start != "" || sp.End != ""
	recurring := sp.Cron != "" || sp.Duration != ""
	switch {
	case oneOff && recurring:
		return w, errors.New("set either start and end (one-off) or cron and duration (recurring), not both")
	case !oneOff && !recurring:
		return w, errors.New("set start and end (one-off) or cron and duration (recurring)")
	case oneOff:
		if sp.TimeZone != "" {
			return w, errors.New("timeZone applies to cron only; start and end carry their own offset")
		}
		var err error
		if w.Start, err = time.Parse(time.RFC3339, sp.Start); err != nil {
			return w, fmt.Errorf("start %q is not an RFC 3339 time (2026-11-21T02:00:00Z)", sp.Start)
		}
		if w.End, err = time.Parse(time.RFC3339, sp.End); err != nil {
			return w, fmt.Errorf("end %q is not an RFC 3339 time (2026-11-21T04:00:00Z)", sp.End)
		}
		w.Start, w.End = w.Start.UTC(), w.End.UTC()
		if !w.End.After(w.Start) {
			return w, errors.New("end must be after start")
		}
		if w.End.Sub(w.Start) > MaxOneOff {
			return w, errors.New("a one-off window can be at most 31 days long")
		}
	default:
		if sp.Cron == "" || sp.Duration == "" {
			return w, errors.New("a recurring window needs both cron and duration")
		}
		if strings.HasPrefix(strings.TrimSpace(sp.Cron), "@") || len(strings.Fields(sp.Cron)) != 5 {
			return w, fmt.Errorf("cron %q must have five fields (minute hour day-of-month month day-of-week)", sp.Cron)
		}
		if strings.Contains(sp.Cron, "TZ=") {
			return w, errors.New("cron must not carry a zone; use timeZone")
		}
		d, err := time.ParseDuration(sp.Duration)
		if err != nil {
			return w, fmt.Errorf("duration %q is not a duration such as 90m or 2h", sp.Duration)
		}
		if d < MinDuration || d > MaxDuration {
			return w, errors.New("duration must be between 1 minute and 168 hours")
		}
		tz := sp.TimeZone
		if tz == "" {
			tz = "UTC"
		}
		// schedule.Parse checks the cron and the zone the way CronJobs are checked; the window keeps its own reading
		// of the schedule (next), which treats daylight saving differently from the CronJob controller.
		sc, err := schedule.Parse(sp.Cron, tz, time.UTC)
		if err != nil {
			return w, err
		}
		spec, err := cron.ParseStandard(sp.Cron)
		if err != nil {
			return w, fmt.Errorf("schedule %q: %w", sp.Cron, err)
		}
		w.Cron, w.TimeZone, w.Duration, w.spec, w.loc = strings.Join(strings.Fields(sp.Cron), " "), tz, d, spec, sc.Location()
		// A cron that never fires (30 February) would be a window nobody can see; it is a mistake, not a choice.
		if next := w.next(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)); next.IsZero() || next.After(time.Date(2004, 1, 1, 0, 0, 0, 0, time.UTC)) {
			return w, fmt.Errorf("cron %q never fires", sp.Cron)
		}
		// An occurrence must end before the next one starts: a window that overlaps itself (* * * * * for 168h) says
		// nothing more than a longer one, and would make every question expand without bound.
		if gap, at := w.shortestInterval(); gap > 0 && d > gap {
			return w, fmt.Errorf("duration %s is longer than the %s between two starts (%s): occurrences would overlap", d, gap, at.In(w.loc).Format("2006-01-02 15:04 MST"))
		}
	}
	return w, nil
}

// shortestInterval is the shortest real time between two consecutive starts of a recurring window, over two sample
// years (each zone's daylight-saving changes, which can bring two starts closer, happen once a year) and at most
// intervalSample starts; at is the first start of that pair. Intervals of a week or more do not matter (the longest
// duration is 168 hours), so a schedule whose starts are all that far apart is not walked to the end.
func (w Window) shortestInterval() (gap time.Duration, at time.Time) {
	const intervalSample = 400_000
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, w.loc)
	end := from.AddDate(2, 0, 7)
	prev := w.next(from)
	for i := 0; i < intervalSample && !prev.IsZero() && prev.Before(end); i++ {
		n := w.next(prev)
		if n.IsZero() {
			break
		}
		if d := n.Sub(prev); gap == 0 || d < gap {
			gap, at = d, prev
			if gap < w.Duration {
				break // already too short: no need to look further
			}
		}
		prev = n
	}
	return gap, at
}

// next is the first start of a recurring window strictly after t. The cron is matched on the wall clock of the
// window's zone, then the wall time is placed in the zone:
//
//   - a wall time the clocks skip (02:30 on the spring-forward night in America/New_York) starts the window at the
//     first instant that exists after it (03:00 EDT), so planned work is not dropped for a night;
//   - a wall time that happens twice (01:30 on the fall-back night) starts it once, at its first occurrence.
//
// This differs on purpose from the CronJob controller (which skips the first and fires twice on the second): a window
// is a declared stretch of planned work, not a run.
func (w Window) next(t time.Time) time.Time {
	lt := t.In(w.loc)
	naive := time.Date(lt.Year(), lt.Month(), lt.Day(), lt.Hour(), lt.Minute(), lt.Second(), lt.Nanosecond(), time.UTC)
	// Each pass moves the wall clock forward by at least a minute; only the repeated hour of a fall-back night (wall
	// times whose first occurrence is before t) takes more than one.
	for i := 0; i < 2000; i++ {
		n := w.spec.Next(naive)
		if n.IsZero() {
			return time.Time{}
		}
		if s := wallInstant(n, w.loc); s.After(t) {
			return s.UTC()
		}
		naive = n
	}
	return time.Time{}
}

// wallInstant places a wall-clock time (given as a UTC time with those fields) in loc: the earliest instant showing
// that wall time, or, for a wall time the clocks skip, the instant the clocks jump (the first valid one after it).
func wallInstant(wall time.Time, loc *time.Location) time.Time {
	sameWall := func(t time.Time) bool {
		l := t.In(loc)
		return l.Year() == wall.Year() && l.YearDay() == wall.YearDay() && l.Hour() == wall.Hour() && l.Minute() == wall.Minute() && l.Second() == wall.Second()
	}
	// The zone's offsets around this wall time: a change within a day either side is the only kind that matters.
	_, before := wall.Add(-26 * time.Hour).In(loc).Zone()
	_, after := wall.Add(26 * time.Hour).In(loc).Zone()
	var best time.Time
	for _, off := range []int{before, after} {
		c := wall.Add(-time.Duration(off) * time.Second)
		if sameWall(c) && (best.IsZero() || c.Before(best)) {
			best = c
		}
	}
	if !best.IsZero() {
		return best
	}
	// A skipped wall time: read with the offset in force before the change it lands after the change; the start of
	// that zone period is the jump itself.
	c := wall.Add(-time.Duration(before) * time.Second)
	start, _ := c.In(loc).ZoneBounds()
	if start.IsZero() || start.After(c) {
		return c
	}
	return start
}

// Len is how many windows are configured.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.windows)
}

// Windows lists the configured windows in the order they were declared.
func (s *Set) Windows() []Window {
	if s == nil {
		return nil
	}
	return append([]Window(nil), s.windows...)
}

func (w Window) occurrence(from time.Time) Occurrence {
	o := Occurrence{Name: w.Name, Note: w.Note, Namespaces: w.Namespaces, Recurring: w.Recurring()}
	if w.spec == nil {
		o.From, o.To = w.Start, w.End
	} else {
		o.From, o.To = from, from.Add(w.Duration)
	}
	return o
}

// occurrences of one window overlapping [from, to), oldest first, at most MaxOccurrences and at most *budget (which
// it spends). truncated says there were more.
func (w Window) occurrences(from, to time.Time, budget *int) (out []Occurrence, truncated bool) {
	if w.spec == nil {
		if w.Start.Before(to) && w.End.After(from) {
			out = append(out, w.occurrence(w.Start))
			*budget--
		}
		return out, false
	}
	// Starts in (from - duration, to): every occurrence that is still open at from, or opens before to.
	for t := w.next(from.Add(-w.Duration)); !t.IsZero() && t.Before(to); t = w.next(t) {
		if len(out) == MaxOccurrences || *budget <= 0 {
			return out, true
		}
		out = append(out, w.occurrence(t))
		*budget--
	}
	return out, false
}

// Between lists the occurrences of every window that overlap [from, to), by start time. truncated says a recurring
// window had more than MaxOccurrences in the range, or all of them more than MaxExpansion, and the rest were not
// expanded: the list is then incomplete and must be said to be.
func (s *Set) Between(from, to time.Time) (out []Occurrence, truncated bool) {
	return s.BetweenIn(from, to, "")
}

// BetweenIn is Between for the windows that cover namespace ns (every window when ns is ""), so windows of other
// namespaces do not spend the expansion budget.
func (s *Set) BetweenIn(from, to time.Time, ns string) (out []Occurrence, truncated bool) {
	if s == nil {
		return nil, false
	}
	budget := MaxExpansion
	for _, w := range s.windows {
		if ns != "" && !w.Covers(ns) {
			continue
		}
		o, tr := w.occurrences(from, to, &budget)
		out = append(out, o...)
		truncated = truncated || tr
	}
	sortOccurrences(out)
	return out, truncated
}

func sortOccurrences(out []Occurrence) {
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].From.Equal(out[j].From) {
			return out[i].From.Before(out[j].From)
		}
		return out[i].Name < out[j].Name
	})
}

// earlier says whether a wins over b when both cover a time: the earlier start, then the name (the same order as
// Between, so Index.At and Set.At agree).
func earlier(a, b Occurrence) bool {
	if !a.From.Equal(b.From) {
		return a.From.Before(b.From)
	}
	return a.Name < b.Name
}

// At is the occurrence covering t in namespace ns, the earliest-starting one when several do.
func (s *Set) At(t time.Time, ns string) (Occurrence, bool) {
	if s == nil {
		return Occurrence{}, false
	}
	var best Occurrence
	found := false
	for _, w := range s.windows {
		if !w.Covers(ns) {
			continue
		}
		if o, ok := w.at(t); ok && (!found || earlier(o, best)) {
			best, found = o, true
		}
	}
	return best, found
}

// at is the occurrence of w open at t, if any. Occurrences of one window do not overlap (validation refuses a duration
// longer than the time between two starts), so the first start after t - duration is the only one that can cover t.
func (w Window) at(t time.Time) (Occurrence, bool) {
	if w.spec == nil {
		if !t.Before(w.Start) && t.Before(w.End) {
			return w.occurrence(w.Start), true
		}
		return Occurrence{}, false
	}
	if s := w.next(t.Add(-w.Duration)); !s.IsZero() && !s.After(t) {
		return w.occurrence(s), true
	}
	return Occurrence{}, false
}

// Active says, for each window, whether it is open at now (in any namespace).
func (s *Set) Active(now time.Time) map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	for _, w := range s.windows {
		_, ok := w.at(now)
		out[w.Name] = ok
	}
	return out
}

// Next lists the current occurrence of w (if open at now) and the next ones, up to n in all.
func (w Window) Next(now time.Time, n int) []Occurrence {
	var out []Occurrence
	if o, ok := w.at(now); ok {
		out = append(out, o)
	}
	if w.spec == nil {
		if now.Before(w.Start) {
			out = append(out, w.occurrence(w.Start))
		}
		return out
	}
	for t := w.next(now); !t.IsZero() && len(out) < n; t = w.next(t) {
		out = append(out, w.occurrence(t))
	}
	return out
}

// Index answers At for many times in one range without expanding a schedule each time.
type Index struct {
	occ       []Occurrence // by start
	longest   time.Duration
	set       *Set
	truncated bool
}

// Index expands the occurrences overlapping [from, to) once. truncated is as for Between: past the cap, a time is
// looked up window by window instead (correct, only slower), so a cut expansion never gives a wrong window.
func (s *Set) Index(from, to time.Time) Index {
	occ, tr := s.Between(from, to)
	ix := Index{occ: occ, set: s, truncated: tr}
	for _, o := range occ {
		ix.longest = max(ix.longest, o.To.Sub(o.From))
	}
	return ix
}

// At is the occurrence covering t in ns, the earliest-starting one when several do.
func (ix Index) At(t time.Time, ns string) (Occurrence, bool) {
	if ix.truncated {
		return ix.set.At(t, ns)
	}
	// The occurrences starting at or before t, found by binary search; only those that started within the longest
	// occurrence before t can still be open. The earliest-starting match is the last one found walking back.
	hi := sort.Search(len(ix.occ), func(i int) bool { return ix.occ[i].From.After(t) })
	var best Occurrence
	found := false
	for i := hi - 1; i >= 0 && t.Sub(ix.occ[i].From) < ix.longest; i-- {
		if ix.occ[i].Covers(t, ns) {
			best, found = ix.occ[i], true
		}
	}
	return best, found
}

// Merge joins occurrences that overlap or touch and cover the same namespaces into one stretch each (names and notes
// are the first one's), for leaving them out of a query: fewer, wider spans, the same set of times.
func Merge(occ []Occurrence) []Occurrence {
	byNS := map[string][]Occurrence{}
	var keys []string
	for _, o := range occ {
		k := strings.Join(o.Namespaces, ",")
		if _, ok := byNS[k]; !ok {
			keys = append(keys, k)
		}
		byNS[k] = append(byNS[k], o)
	}
	var out []Occurrence
	for _, k := range keys {
		list := byNS[k]
		sort.SliceStable(list, func(i, j int) bool { return list[i].From.Before(list[j].From) })
		cur := list[0]
		for _, o := range list[1:] {
			if !o.From.After(cur.To) {
				if o.To.After(cur.To) {
					cur.To = o.To
				}
				continue
			}
			out = append(out, cur)
			cur = o
		}
		out = append(out, cur)
	}
	sortOccurrences(out)
	return out
}

// Truncated says the index's expansion was cut (Occurrences is then incomplete).
func (ix Index) Truncated() bool { return ix.truncated }

// Occurrences are the occurrences the index holds.
func (ix Index) Occurrences() []Occurrence { return ix.occ }
