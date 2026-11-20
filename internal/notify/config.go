// Package notify sends the morning summary to the destinations configured in the chart: Slack, Microsoft Teams, or a
// webhook of your own. It is the one thing OpsTide sends out of the cluster, and only when configured. Destinations
// and their schedule are read from a file at start (the chart renders it); a destination's URL is read from a file
// mounted from a Secret each time a message is sent, so rotating it needs no restart, and it is never logged, shown
// or returned.
package notify

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"sigs.k8s.io/yaml"

	"github.com/mihnk/opstide/internal/digest"
	"github.com/mihnk/opstide/internal/schedule"
)

// Destination types.
const (
	Slack   = "slack"
	Teams   = "teams"
	Webhook = "webhook"
)

// MaxDestinations bounds how many places one summary goes.
const MaxDestinations = 10

// File is the configuration file the chart renders from notify.* values.
type File struct {
	MorningSummary SummarySpec       `json:"morningSummary"`
	Destinations   []DestinationSpec `json:"destinations"`
}

// SummarySpec says when the summary is sent and what it says.
type SummarySpec struct {
	Schedule string   `json:"schedule"` // five-field cron, read in TimeZone
	TimeZone string   `json:"timeZone"` // IANA zone; UTC when empty
	Covers   string   `json:"covers"`   // since-18h (default), 12h or 24h
	Language string   `json:"language"` // en (default) or tr
	Include  []string `json:"include"`  // empty: the defaults
}

// DestinationSpec is one place the summary goes. URLFile holds its URL (a mounted Secret key); Source says, for
// people, where that is ("Secret opstide-notify, key slack").
type DestinationSpec struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	URLFile string `json:"urlFile"`
	Source  string `json:"source,omitempty"`
}

// Config is a validated File.
type Config struct {
	Summary      SummarySpec
	Location     *time.Location
	Schedule     *schedule.Schedule
	Destinations []DestinationSpec
}

const maxFileBytes = 64 << 10

// LoadFile reads and validates the configuration. An empty path means no morning summary (nil, nil).
func LoadFile(path string) (*Config, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("notify file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("notify file %s: %w", path, err)
	}
	if len(b) > maxFileBytes {
		return nil, fmt.Errorf("notify file %s is larger than 64 KiB", path)
	}
	c, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("notify file %s: %w", path, err)
	}
	return c, nil
}

// Parse reads YAML (or JSON) and validates it, the destinations' URL files included. Anything unknown or unusable is
// an error: OpsTide refuses to start rather than send somewhere unintended, or not send at all.
func Parse(b []byte) (*Config, error) {
	var f File
	if err := yaml.UnmarshalStrict(b, &f); err != nil {
		return nil, fmt.Errorf("cannot read it: %w", err)
	}
	s := f.MorningSummary
	if s.Schedule == "" {
		s.Schedule = "30 8 * * 1-5"
	}
	if s.TimeZone == "" {
		s.TimeZone = "UTC"
	}
	if s.Covers == "" {
		s.Covers = digest.CoversSince18
	}
	if s.Language == "" {
		s.Language = "en"
	}
	if len(s.Include) == 0 {
		s.Include = slices.Clone(digest.DefaultIncludes)
	}
	c := &Config{Summary: s}
	var err error
	if c.Location, err = time.LoadLocation(s.TimeZone); err != nil {
		return nil, fmt.Errorf("morningSummary.timeZone %q: %w", s.TimeZone, err)
	}
	if c.Schedule, err = schedule.Parse(s.Schedule, s.TimeZone, nil); err != nil {
		return nil, fmt.Errorf("morningSummary.schedule: %w", err)
	}
	if c.Schedule.Next(time.Now()).IsZero() {
		return nil, fmt.Errorf("morningSummary.schedule %q never fires", s.Schedule)
	}
	if !digest.ValidCovers(s.Covers) {
		return nil, fmt.Errorf("morningSummary.covers must be since-18h, 12h or 24h, not %q", s.Covers)
	}
	if s.Language != "en" && s.Language != "tr" {
		return nil, fmt.Errorf("morningSummary.language must be en or tr, not %q", s.Language)
	}
	for _, p := range s.Include {
		if !slices.Contains(digest.AllIncludes, p) {
			return nil, fmt.Errorf("morningSummary.include: %q is not one of %s", p, strings.Join(digest.AllIncludes, ", "))
		}
	}
	if len(f.Destinations) == 0 {
		return nil, errors.New("no destinations: list at least one, or leave notify out")
	}
	if len(f.Destinations) > MaxDestinations {
		return nil, fmt.Errorf("%d destinations; at most %d are allowed", len(f.Destinations), MaxDestinations)
	}
	seen := map[string]bool{}
	for i, d := range f.Destinations {
		if d.Name == "" || utf8.RuneCountInString(d.Name) > 80 || !utf8.ValidString(d.Name) {
			return nil, fmt.Errorf("destination #%d needs a name of at most 80 characters", i+1)
		}
		if seen[d.Name] {
			return nil, fmt.Errorf("destination %q is declared twice", d.Name)
		}
		seen[d.Name] = true
		if d.Type != Slack && d.Type != Teams && d.Type != Webhook {
			return nil, fmt.Errorf("destination %q: type must be slack, teams or webhook", d.Name)
		}
		if d.URLFile == "" {
			return nil, fmt.Errorf("destination %q: urlFile is empty", d.Name)
		}
		if _, err := readURL(d); err != nil {
			return nil, fmt.Errorf("destination %q: %w", d.Name, err)
		}
	}
	c.Destinations = f.Destinations
	return c, nil
}

// readURL reads and checks a destination's URL. Errors never contain it.
func readURL(d DestinationSpec) (string, error) {
	b, err := os.ReadFile(d.URLFile)
	if err != nil {
		var pe *os.PathError
		if errors.As(err, &pe) {
			return "", fmt.Errorf("cannot read its URL file %s: %v", d.URLFile, pe.Err)
		}
		return "", errors.New("cannot read its URL file")
	}
	raw := strings.TrimSpace(string(b))
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the URL in %s is not a URL", d.URLFile)
	}
	switch {
	case d.Type == Webhook && u.Scheme != "https" && u.Scheme != "http":
		return "", fmt.Errorf("the URL in %s must be http or https", d.URLFile)
	case d.Type != Webhook && u.Scheme != "https":
		return "", fmt.Errorf("the URL in %s must be https", d.URLFile)
	}
	return raw, nil
}
