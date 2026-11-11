// Package release holds OpsTide's release logic as plain functions: what a
// revision's snapshot contains, how two snapshots differ, how a Pod's
// lifecycle is read, how a rollout is classified and how a release's health
// is assessed. It never talks to Kubernetes or to the database, so the
// collector and the API apply exactly the same rules.
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// Snapshot is an explicit allow-list of a revision's configuration. It holds
// names, images, sizes and probe settings, never a Secret value, a literal
// environment value, a command line, a token or a whole spec.
type Snapshot struct {
	Containers     []Container       `json:"containers"`
	InitContainers []Container       `json:"initContainers,omitempty"`
	ConfigMaps     []string          `json:"configMaps,omitempty"` // names only
	Secrets        []string          `json:"secrets,omitempty"`    // names only, never contents
	ServiceAccount string            `json:"serviceAccount,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	// Deployment holds the Deployment's own settings as they were when the
	// release was first seen. It is nil for a release that was adopted: the
	// Deployment's current spec says nothing reliable about an older revision.
	Deployment *DeploymentFields `json:"deployment,omitempty"`
}

// DeploymentFields are the Deployment-level settings a release can be
// compared on.
type DeploymentFields struct {
	Replicas                *int32 `json:"replicas,omitempty"`
	StrategyType            string `json:"strategyType,omitempty"`
	MaxSurge                string `json:"maxSurge,omitempty"`
	MaxUnavailable          string `json:"maxUnavailable,omitempty"`
	MinReadySeconds         int32  `json:"minReadySeconds,omitempty"`
	ProgressDeadlineSeconds *int32 `json:"progressDeadlineSeconds,omitempty"`
}

// Container is one container's allow-listed configuration.
type Container struct {
	Name      string            `json:"name"`
	Image     string            `json:"image"`
	Requests  map[string]string `json:"requests,omitempty"` // cpu, memory
	Limits    map[string]string `json:"limits,omitempty"`
	Readiness *Probe            `json:"readiness,omitempty"`
	Liveness  *Probe            `json:"liveness,omitempty"`
	Startup   *Probe            `json:"startup,omitempty"`
	EnvNames  []string          `json:"envNames,omitempty"` // names only
}

// Probe is a probe's behaviour. An exec command is reduced to a short hash
// (a change shows, the command, which may hold credentials, is not kept) and
// an HTTP path loses its query string.
type Probe struct {
	Kind         string `json:"kind"` // httpGet, tcpSocket, exec, grpc, other
	Path         string `json:"path,omitempty"`
	Port         string `json:"port,omitempty"`
	Scheme       string `json:"scheme,omitempty"`
	CommandHash  string `json:"commandHash,omitempty"`
	InitialDelay int32  `json:"initialDelaySeconds,omitempty"`
	Period       int32  `json:"periodSeconds,omitempty"`
	Timeout      int32  `json:"timeoutSeconds,omitempty"`
	Success      int32  `json:"successThreshold,omitempty"`
	Failure      int32  `json:"failureThreshold,omitempty"`
}

// Labels kept from the Pod template: they identify the workload and its
// version and nothing else.
var labelAllow = map[string]bool{
	"app": true, "version": true,
	"app.kubernetes.io/name": true, "app.kubernetes.io/instance": true,
	"app.kubernetes.io/version": true, "app.kubernetes.io/part-of": true, "app.kubernetes.io/component": true,
}

const (
	maxContainers = 32
	maxEnvNames   = 200
	maxText       = 253
)

// FromTemplate builds the snapshot of a Pod template. d is the Deployment
// when the template is the revision being observed live, and nil when the
// release is adopted.
func FromTemplate(t corev1.PodTemplateSpec, d *appsv1.Deployment) Snapshot {
	s := Snapshot{ServiceAccount: clip(t.Spec.ServiceAccountName)}
	cms, secs := map[string]bool{}, map[string]bool{}
	addRef := func(set map[string]bool, name string) {
		if name != "" {
			set[clip(name)] = true
		}
	}
	for _, c := range cap32(t.Spec.Containers) {
		s.Containers = append(s.Containers, fromContainer(c, cms, secs))
	}
	for _, c := range cap32(t.Spec.InitContainers) {
		s.InitContainers = append(s.InitContainers, fromContainer(c, cms, secs))
	}
	for _, v := range t.Spec.Volumes {
		if v.ConfigMap != nil {
			addRef(cms, v.ConfigMap.Name)
		}
		if v.Secret != nil {
			addRef(secs, v.Secret.SecretName)
		}
		if v.Projected != nil {
			for _, src := range v.Projected.Sources {
				if src.ConfigMap != nil {
					addRef(cms, src.ConfigMap.Name)
				}
				if src.Secret != nil {
					addRef(secs, src.Secret.Name)
				}
			}
		}
	}
	for _, ref := range t.Spec.ImagePullSecrets {
		addRef(secs, ref.Name)
	}
	s.ConfigMaps, s.Secrets = keys(cms), keys(secs)
	for k, v := range t.Labels {
		if labelAllow[k] {
			if s.Labels == nil {
				s.Labels = map[string]string{}
			}
			s.Labels[k] = clip(v)
		}
	}
	if d != nil {
		f := &DeploymentFields{Replicas: d.Spec.Replicas, MinReadySeconds: d.Spec.MinReadySeconds, ProgressDeadlineSeconds: d.Spec.ProgressDeadlineSeconds,
			StrategyType: string(d.Spec.Strategy.Type)}
		if ru := d.Spec.Strategy.RollingUpdate; ru != nil {
			if ru.MaxSurge != nil {
				f.MaxSurge = ru.MaxSurge.String()
			}
			if ru.MaxUnavailable != nil {
				f.MaxUnavailable = ru.MaxUnavailable.String()
			}
		}
		s.Deployment = f
	}
	return s
}

func fromContainer(c corev1.Container, cms, secs map[string]bool) Container {
	out := Container{Name: clip(c.Name), Image: clip(c.Image)}
	out.Requests, out.Limits = resources(c.Resources.Requests), resources(c.Resources.Limits)
	out.Readiness, out.Liveness, out.Startup = probe(c.ReadinessProbe), probe(c.LivenessProbe), probe(c.StartupProbe)
	names := map[string]bool{}
	for _, e := range c.Env {
		names[clip(e.Name)] = true
		if r := e.ValueFrom; r != nil {
			if r.ConfigMapKeyRef != nil && r.ConfigMapKeyRef.Name != "" {
				cms[clip(r.ConfigMapKeyRef.Name)] = true
			}
			if r.SecretKeyRef != nil && r.SecretKeyRef.Name != "" {
				secs[clip(r.SecretKeyRef.Name)] = true
			}
		}
	}
	for _, ef := range c.EnvFrom {
		if ef.ConfigMapRef != nil && ef.ConfigMapRef.Name != "" {
			cms[clip(ef.ConfigMapRef.Name)] = true
		}
		if ef.SecretRef != nil && ef.SecretRef.Name != "" {
			secs[clip(ef.SecretRef.Name)] = true
		}
	}
	out.EnvNames = keys(names)
	if len(out.EnvNames) > maxEnvNames {
		out.EnvNames = out.EnvNames[:maxEnvNames]
	}
	return out
}

func resources(l corev1.ResourceList) map[string]string {
	var m map[string]string
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if q, ok := l[name]; ok {
			if m == nil {
				m = map[string]string{}
			}
			m[string(name)] = q.String()
		}
	}
	return m
}

func probe(p *corev1.Probe) *Probe {
	if p == nil {
		return nil
	}
	out := &Probe{InitialDelay: p.InitialDelaySeconds, Period: p.PeriodSeconds, Timeout: p.TimeoutSeconds, Success: p.SuccessThreshold, Failure: p.FailureThreshold}
	switch {
	case p.HTTPGet != nil:
		out.Kind, out.Port, out.Scheme = "httpGet", p.HTTPGet.Port.String(), string(p.HTTPGet.Scheme)
		path, _, _ := strings.Cut(p.HTTPGet.Path, "?")
		out.Path = clip(path)
	case p.TCPSocket != nil:
		out.Kind, out.Port = "tcpSocket", p.TCPSocket.Port.String()
	case p.Exec != nil:
		out.Kind = "exec"
		sum := sha256.Sum256([]byte(strings.Join(p.Exec.Command, "\x00")))
		out.CommandHash = hex.EncodeToString(sum[:])[:10]
	case p.GRPC != nil:
		out.Kind, out.Port = "grpc", fmt.Sprint(p.GRPC.Port)
	default:
		out.Kind = "other"
	}
	return out
}

func cap32[T any](s []T) []T {
	if len(s) > maxContainers {
		return s[:maxContainers]
	}
	return s
}

// clip cuts s to maxText bytes on a character boundary, so a stored name or image stays valid UTF-8.
func clip(s string) string {
	if len(s) <= maxText {
		return s
	}
	n := maxText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func keys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Normalize orders everything that has no meaningful order, so equal
// configurations produce equal bytes.
func (s Snapshot) Normalize() Snapshot {
	sortC := func(cs []Container) []Container {
		out := append([]Container(nil), cs...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		for i := range out {
			out[i].EnvNames = append([]string(nil), out[i].EnvNames...)
			sort.Strings(out[i].EnvNames)
		}
		return out
	}
	s.Containers, s.InitContainers = sortC(s.Containers), sortC(s.InitContainers)
	s.ConfigMaps, s.Secrets = append([]string(nil), s.ConfigMaps...), append([]string(nil), s.Secrets...)
	sort.Strings(s.ConfigMaps)
	sort.Strings(s.Secrets)
	return s
}

// Fingerprint identifies the Pod-template part of a snapshot: the same
// configuration gives the same value, so a rollback to an old template is
// recognised. The Deployment-level settings are left out of it.
func (s Snapshot) Fingerprint() string {
	n := s.Normalize()
	n.Deployment = nil
	b, _ := json.Marshal(n)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// Encode is the stored form of a snapshot.
func (s Snapshot) Encode() string {
	b, _ := json.Marshal(s.Normalize())
	return string(b)
}

// DecodeSnapshot reads a stored snapshot.
func DecodeSnapshot(raw string) (Snapshot, error) {
	var s Snapshot
	err := json.Unmarshal([]byte(raw), &s)
	return s, err
}

// ID is a release's stable identifier: the same Deployment, ReplicaSet and
// revision always give the same one. A rollback reuses the old ReplicaSet
// under a new revision number, so it is a release of its own.
func ID(deploymentUID, replicaSetUID, revision string) string {
	sum := sha256.Sum256([]byte(deploymentUID + "|" + replicaSetUID + "|" + revision))
	return "r" + hex.EncodeToString(sum[:])[:20]
}
