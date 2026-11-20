package digest

import (
	"fmt"
	"strings"
	"time"
)

// Line is one line of a summary as people read it. Kind is heading, text, item (a bullet) or note (quieter).
type Line struct {
	Kind  string `json:"kind"`
	Parts []Part `json:"parts"`
}

// Part is a run of text in a line, with how to show it. Link, when set, makes it a link.
type Part struct {
	Text string `json:"text"`
	Bold bool   `json:"bold,omitempty"`
	Fail bool   `json:"fail,omitempty"`
	Mono bool   `json:"mono,omitempty"`
	Link string `json:"link,omitempty"`
}

// Plain is the line as plain text, links left out.
func (l Line) Plain() string {
	var b strings.Builder
	for _, p := range l.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// words is the vocabulary of a summary in one language. Each entry is a format for fmt.Sprintf.
type words struct {
	heading, test, late, runs, noRuns, unknown, changes, failed, during, overdue, lastOK, noOK, open  string
	troubleAfterBy, troubleAfter, troubleMore, changed, more, notWatched, quiet, due, dueNone, footer string
	overview                                                                                          string
	yesterday                                                                                         string
	days                                                                                              [7]string
	unit                                                                                              func(n int64, u string) string
}

var english = words{
	heading:        "Morning summary · %s · since %s",
	test:           "Test message · ",
	late:           "Sent late: OpsTide was not running at its time.",
	runs:           "%d runs: %d succeeded, %d failed.",
	noRuns:         "No CronJob ran.",
	unknown:        " %d with an outcome not known.",
	changes:        " %d changes recorded.",
	failed:         "Failed",
	during:         ", during maintenance %s",
	overdue:        "Overdue",
	lastOK:         ": should succeed every %s, last success %s ago",
	noOK:           ": should succeed every %s, no success recorded",
	open:           "open",
	troubleAfterBy: " · %[1]s after a change by %[2]s",
	troubleAfter:   " · %s after: %s",
	troubleMore:    " (%d changes in the hour before)",
	changed:        "Changed",
	more:           "…and %d more",
	notWatched:     "Not watched for part of this time: %s. Nothing is said about them then.",
	quiet:          "Nothing needs a look.",
	due:            "Due today: %d more, next %s at %s.",
	dueNone:        "Nothing more is due today.",
	footer:         "Read-only record from OpsTide. Times in %s.",
	overview:       "Overview",
	yesterday:      "yesterday",
	days:           [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"},
	unit: func(n int64, u string) string {
		return fmt.Sprintf("%d %s", n, map[string]string{"d": "d", "h": "h", "m": "min"}[u])
	},
}

var turkish = words{
	heading:        "Sabah özeti · %s · %s itibarıyla",
	test:           "Deneme mesajı · ",
	late:           "Geç gönderildi: OpsTide zamanında çalışmıyordu.",
	runs:           "%d çalışma: %d başarılı, %d başarısız.",
	noRuns:         "Hiçbir CronJob çalışmadı.",
	unknown:        " %d çalışmanın sonucu bilinmiyor.",
	changes:        " %d değişiklik kaydedildi.",
	failed:         "Başarısız",
	during:         ", %s bakımı sırasında",
	overdue:        "Gecikti",
	lastOK:         ": her %s içinde başarılı olmalı, son başarı %s önce",
	noOK:           ": her %s içinde başarılı olmalı, kayıtlı başarı yok",
	open:           "aç",
	troubleAfterBy: " · %[2]s tarafından yapılan bir değişiklikten %[1]s sonra",
	troubleAfter:   " · %s sonra: %s",
	troubleMore:    " (önceki bir saatte %d değişiklik)",
	changed:        "Değişti",
	more:           "…ve %d tane daha",
	notWatched:     "Bu sürenin bir kısmında izlenmedi: %s. O sırada onlar hakkında bir şey söylenmiyor.",
	quiet:          "Bakılması gereken bir şey yok.",
	due:            "Bugün kalan: %d çalışma, sıradaki %s, %s.",
	dueNone:        "Bugün başka beklenen çalışma yok.",
	footer:         "OpsTide'ın salt-okunur kaydı. Saatler %s.",
	overview:       "Genel bakış",
	yesterday:      "dün",
	days:           [7]string{"Paz", "Pzt", "Sal", "Çar", "Per", "Cum", "Cmt"},
	unit: func(n int64, u string) string {
		return fmt.Sprintf("%d %s", n, map[string]string{"d": "g", "h": "sa", "m": "dk"}[u])
	},
}

func vocabulary(lang string) words {
	if lang == "tr" {
		return turkish
	}
	return english
}

// Lines is the summary as people read it, in its language.
func (m Morning) Lines() []Line {
	w := vocabulary(m.Language)
	loc, err := time.LoadLocation(m.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	at := func(t time.Time) string { return clock(t, m.To, loc, w) }
	var out []Line
	add := func(kind string, parts ...Part) { out = append(out, Line{Kind: kind, Parts: parts}) }
	text := func(s string) Part { return Part{Text: s} }

	head := fmt.Sprintf(w.heading, m.Cluster, at(m.From))
	if m.Test {
		head = w.test + head
	}
	add("heading", Part{Text: head, Bold: true})
	if m.Late {
		add("note", text(w.late))
	}

	r := m.Runs
	var runs []Part
	if r.Total == 0 {
		runs = append(runs, text(w.noRuns))
	} else {
		s := fmt.Sprintf(w.runs, r.Total, r.Succeeded, r.Failed)
		runs = append(runs, Part{Text: s, Bold: r.Failed > 0})
		if r.Unknown > 0 {
			runs = append(runs, text(fmt.Sprintf(w.unknown, r.Unknown)))
		}
	}
	if m.Changes > 0 {
		runs = append(runs, text(fmt.Sprintf(w.changes, m.Changes)))
	}
	add("text", runs...)

	if m.Includes(IncludeFailures) {
		for _, f := range m.Failed {
			parts := []Part{{Text: w.failed, Bold: true, Fail: true}, text(" " + f.Name + " "), {Text: f.Namespace + "/" + f.CronJob, Mono: true}, text(", " + at(f.At))}
			if f.Maintenance != "" {
				parts = append(parts, text(fmt.Sprintf(w.during, f.Maintenance)))
			}
			if f.Link != "" {
				parts = append(parts, text(" · "), Part{Text: w.open, Link: f.Link})
			}
			add("item", parts...)
		}
		if m.FailedMore > 0 {
			add("item", text(fmt.Sprintf(w.more, m.FailedMore)))
		}
		for _, o := range m.Overdue {
			within := span(o.WithinSeconds, w)
			rest := fmt.Sprintf(w.noOK, within)
			if !o.LastSuccess.IsZero() {
				rest = fmt.Sprintf(w.lastOK, within, span(int64(m.To.Sub(o.LastSuccess)/time.Second), w))
			}
			parts := []Part{{Text: w.overdue, Bold: true, Fail: true}, text(" " + o.Name + rest)}
			if o.Link != "" {
				parts = append(parts, text(" · "), Part{Text: w.open, Link: o.Link})
			}
			add("item", parts...)
		}
		if m.OverdueMore > 0 {
			add("item", text(fmt.Sprintf(w.more, m.OverdueMore)))
		}
	}
	if m.Includes(IncludeTroubles) {
		for _, t := range m.Troubles {
			parts := []Part{{Text: t.Summary, Bold: true}, text(", " + at(t.At))}
			if n := t.Nearest; n != nil {
				gap := span(n.SecondsBefore, w)
				if n.Writer != "" {
					parts = append(parts, text(fmt.Sprintf(w.troubleAfterBy, gap, n.Writer)))
				} else {
					parts = append(parts, text(fmt.Sprintf(w.troubleAfter, gap, n.Summary)))
				}
				if t.ChangesBefore > 1 {
					parts = append(parts, text(fmt.Sprintf(w.troubleMore, t.ChangesBefore)))
				}
			}
			if t.Link != "" {
				parts = append(parts, text(" · "), Part{Text: w.open, Link: t.Link})
			}
			add("item", parts...)
		}
		if m.TroubleMore > 0 {
			add("item", text(fmt.Sprintf(w.more, m.TroubleMore)))
		}
	}
	if m.Includes(IncludeChanges) {
		for _, c := range m.ChangeList {
			parts := []Part{{Text: w.changed, Bold: true}, text(" " + c.Summary + ", " + at(c.At))}
			if c.Link != "" {
				parts = append(parts, text(" · "), Part{Text: w.open, Link: c.Link})
			}
			add("item", parts...)
		}
		if m.ChangesMore > 0 {
			add("item", text(fmt.Sprintf(w.more, m.ChangesMore)))
		}
	}
	if m.Includes(IncludeGaps) && len(m.NotWatched) > 0 {
		add("note", text(fmt.Sprintf(w.notWatched, strings.Join(m.NotWatched, ", "))))
	}
	if !m.NeedsLook() {
		add("text", text(w.quiet))
	}
	if m.Includes(IncludeDue) {
		if m.Due.Count > 0 && m.Due.NextName != "" {
			add("text", text(fmt.Sprintf(w.due, m.Due.Count, m.Due.NextName, at(m.Due.NextAt))))
		} else {
			add("text", text(w.dueNone))
		}
	}
	footer := []Part{text(fmt.Sprintf(w.footer, m.TimeZone))}
	if m.Link != "" {
		footer = append(footer, text(" · "), Part{Text: w.overview, Link: m.Link})
	}
	add("note", footer...)
	return out
}

// clock writes t for a reader on the day of ref: "06:47" on that day, "yesterday 23:00" the day before, else the
// weekday.
func clock(t, ref time.Time, loc *time.Location, w words) string {
	lt, lr := t.In(loc), ref.In(loc)
	hm := lt.Format("15:04")
	y1, m1, d1 := lt.Date()
	y2, m2, d2 := lr.Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return hm
	}
	py, pm, pd := lr.AddDate(0, 0, -1).Date()
	if y1 == py && m1 == pm && d1 == pd {
		return w.yesterday + " " + hm
	}
	return w.days[lt.Weekday()] + " " + hm
}

// span writes a length the way people say it: 8 d, 26 h, 45 min.
func span(seconds int64, w words) string {
	switch {
	case seconds >= 2*86400 && seconds%86400 == 0:
		return w.unit(seconds/86400, "d")
	case seconds >= 2*86400:
		return w.unit((seconds+43200)/86400, "d")
	case seconds >= 3600:
		return w.unit((seconds+1800)/3600, "h")
	default:
		m := (seconds + 30) / 60
		if m < 1 {
			m = 1
		}
		return w.unit(m, "m")
	}
}
