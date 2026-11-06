package release

import (
	"fmt"
	"sort"
	"strings"
)

// Change is one difference between two snapshots.
type Change struct {
	// Kind is changed, added or removed.
	Kind string `json:"kind"`
	// Field names what differs: image, memory limit, readiness probe, ...
	Field     string `json:"field"`
	Container string `json:"container,omitempty"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
}

// Summary reads a change as a sentence fragment.
func (c Change) Summary() string {
	who := c.Field
	if c.Container != "" {
		who = c.Container + " " + c.Field
	}
	switch c.Kind {
	case "added":
		return fmt.Sprintf("%s added: %s", who, c.After)
	case "removed":
		return fmt.Sprintf("%s removed: %s", who, c.Before)
	}
	return fmt.Sprintf("%s: %s → %s", who, orNone(c.Before), orNone(c.After))
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Diff lists what changed from prev to cur. Settings that exist on only one
// side are not compared when they are Deployment-level and unknown on the
// other: an adopted release carries none, and nothing is reconstructed.
func Diff(prev, cur Snapshot) []Change {
	prev, cur = prev.Normalize(), cur.Normalize()
	var out []Change
	diffContainers := func(group string, a, b []Container) {
		byName := func(cs []Container) map[string]Container {
			m := map[string]Container{}
			for _, c := range cs {
				m[c.Name] = c
			}
			return m
		}
		am, bm := byName(a), byName(b)
		names := map[string]bool{}
		for n := range am {
			names[n] = true
		}
		for n := range bm {
			names[n] = true
		}
		sorted := make([]string, 0, len(names))
		for n := range names {
			sorted = append(sorted, n)
		}
		sort.Strings(sorted)
		for _, n := range sorted {
			x, hadX := am[n]
			y, hadY := bm[n]
			label := n
			if group != "" {
				label = group + " " + n
			}
			switch {
			case !hadX:
				out = append(out, Change{Kind: "added", Field: "container", Container: label, After: y.Image})
			case !hadY:
				out = append(out, Change{Kind: "removed", Field: "container", Container: label, Before: x.Image})
			default:
				out = append(out, diffContainer(label, x, y)...)
			}
		}
	}
	diffContainers("", prev.Containers, cur.Containers)
	diffContainers("init", prev.InitContainers, cur.InitContainers)
	out = append(out, setDiff("ConfigMap reference", prev.ConfigMaps, cur.ConfigMaps)...)
	out = append(out, setDiff("Secret reference (name only)", prev.Secrets, cur.Secrets)...)
	if prev.ServiceAccount != cur.ServiceAccount {
		out = append(out, change("service account", "", prev.ServiceAccount, cur.ServiceAccount))
	}
	for _, k := range []string{"app.kubernetes.io/version", "version", "app.kubernetes.io/name", "app.kubernetes.io/instance", "app"} {
		if prev.Labels[k] != cur.Labels[k] {
			out = append(out, change("label "+k, "", prev.Labels[k], cur.Labels[k]))
		}
	}
	if prev.Deployment != nil && cur.Deployment != nil {
		a, b := prev.Deployment, cur.Deployment
		if p, c := ptr(a.Replicas), ptr(b.Replicas); p != c {
			out = append(out, change("replicas", "", p, c))
		}
		if a.StrategyType != b.StrategyType {
			out = append(out, change("rollout strategy", "", a.StrategyType, b.StrategyType))
		}
		if a.MaxSurge != b.MaxSurge {
			out = append(out, change("rollout maxSurge", "", a.MaxSurge, b.MaxSurge))
		}
		if a.MaxUnavailable != b.MaxUnavailable {
			out = append(out, change("rollout maxUnavailable", "", a.MaxUnavailable, b.MaxUnavailable))
		}
		if a.MinReadySeconds != b.MinReadySeconds {
			out = append(out, change("minReadySeconds", "", fmt.Sprint(a.MinReadySeconds), fmt.Sprint(b.MinReadySeconds)))
		}
		if p, c := ptr(a.ProgressDeadlineSeconds), ptr(b.ProgressDeadlineSeconds); p != c {
			out = append(out, change("progressDeadlineSeconds", "", p, c))
		}
	}
	return out
}

// DeploymentFieldsCompared says whether the Deployment-level settings were
// known on both sides, for the "not compared" note.
func DeploymentFieldsCompared(prev, cur Snapshot) bool {
	return prev.Deployment != nil && cur.Deployment != nil
}

func ptr(p *int32) string {
	if p == nil {
		return ""
	}
	return fmt.Sprint(*p)
}

func change(field, container, before, after string) Change {
	kind := "changed"
	switch {
	case before == "" && after != "":
		kind = "added"
	case before != "" && after == "":
		kind = "removed"
	}
	return Change{Kind: kind, Field: field, Container: container, Before: before, After: after}
}

func diffContainer(name string, a, b Container) []Change {
	var out []Change
	if a.Image != b.Image {
		out = append(out, change("image", name, a.Image, b.Image))
	}
	for _, r := range []struct {
		field string
		x, y  map[string]string
	}{{"CPU request", a.Requests, b.Requests}, {"memory request", a.Requests, b.Requests}, {"CPU limit", a.Limits, b.Limits}, {"memory limit", a.Limits, b.Limits}} {
		key := "cpu"
		if strings.HasPrefix(r.field, "memory") {
			key = "memory"
		}
		if r.x[key] != r.y[key] {
			out = append(out, change(r.field, name, r.x[key], r.y[key]))
		}
	}
	for _, p := range []struct {
		field string
		x, y  *Probe
	}{{"readiness probe", a.Readiness, b.Readiness}, {"liveness probe", a.Liveness, b.Liveness}, {"startup probe", a.Startup, b.Startup}} {
		if probeText(p.x) != probeText(p.y) {
			out = append(out, change(p.field, name, probeText(p.x), probeText(p.y)))
		}
	}
	out = append(out, setDiffNamed("environment variable name", name, a.EnvNames, b.EnvNames)...)
	return out
}

func probeText(p *Probe) string {
	if p == nil {
		return ""
	}
	var what string
	switch p.Kind {
	case "httpGet":
		what = fmt.Sprintf("httpGet %s:%s", p.Path, p.Port)
		if p.Scheme == "HTTPS" {
			what = "https " + what[len("httpGet "):]
		}
	case "tcpSocket":
		what = "tcp :" + p.Port
	case "exec":
		what = "exec #" + p.CommandHash
	default:
		what = p.Kind
		if p.Port != "" {
			what += " :" + p.Port
		}
	}
	return fmt.Sprintf("%s, delay %ds, period %ds, timeout %ds, success %d, failure %d", what, p.InitialDelay, p.Period, p.Timeout, p.Success, p.Failure)
}

func setDiff(field string, a, b []string) []Change { return setDiffNamed(field, "", a, b) }

func setDiffNamed(field, container string, a, b []string) []Change {
	in := func(s []string) map[string]bool {
		m := map[string]bool{}
		for _, v := range s {
			m[v] = true
		}
		return m
	}
	am, bm := in(a), in(b)
	var out []Change
	for _, v := range b {
		if !am[v] {
			out = append(out, Change{Kind: "added", Field: field, Container: container, After: v})
		}
	}
	for _, v := range a {
		if !bm[v] {
			out = append(out, Change{Kind: "removed", Field: field, Container: container, Before: v})
		}
	}
	return out
}
