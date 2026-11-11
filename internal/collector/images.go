package collector

import (
	"context"
	"encoding/json"
	"sync"
	"time"
	"unicode/utf8"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/mihnk/opstide/internal/store"
)

// A CronJob's job template has no revision history in Kubernetes, so for the image inventory OpsTide keeps which
// image each container of the template named, and from when to when. Image references only.

const (
	maxImageRef      = 512
	maxContainerName = 253
	maxContainers    = 32
)

// imageSeen remembers what was last saved per CronJob, so an unchanged template costs no write.
type imageSeen struct {
	mu   sync.Mutex
	last map[string]string
}

func (s *imageSeen) same(uid, fp string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last[uid] == fp
}

func (s *imageSeen) set(uid, fp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		s.last = map[string]string{}
	}
	s.last[uid] = fp
}

func (s *imageSeen) forget(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.last, uid)
}

// templateContainers are the containers of a Pod template with their images, init containers marked.
func templateContainers(t corev1.PodTemplateSpec) []store.TemplateContainer {
	var out []store.TemplateContainer
	add := func(cs []corev1.Container, init bool) {
		for i, c := range cs {
			if i == maxContainers {
				break
			}
			// Cut on a character boundary, so what is stored is still valid UTF-8 (a reference cut inside a character
			// could not be looked up again); the inventory flags a reference that long as possibly cut.
			img, name := cutUTF8(c.Image, maxImageRef), cutUTF8(c.Name, maxContainerName)
			out = append(out, store.TemplateContainer{Name: name, Init: init, Image: img})
		}
	}
	add(t.Spec.InitContainers, true)
	add(t.Spec.Containers, false)
	return out
}

// recordTemplateImages saves the images a CronJob's job template names, when they changed since last saved. A
// CronJob first recorded long after it was created was already running its images: those rows are adopted.
func (c *Collector) recordTemplateImages(ctx context.Context, cj *batchv1.CronJob) error {
	cs := templateContainers(cj.Spec.JobTemplate.Spec.Template)
	b, _ := json.Marshal(cs)
	uid := string(cj.UID)
	fp := cj.Namespace + "/" + cj.Name + "|" + string(b)
	if c.images.same(uid, fp) {
		return nil
	}
	now := c.now()
	// When the CronJob was last synced: a change found now was made since then, and if OpsTide was not watching the
	// CronJobs of its namespace throughout, it may have been made while it was not (found at the first sync after).
	op, known, err := c.db.Operation(ctx, uid)
	if err != nil {
		return err
	}
	scope := scopeName(CronJobs, cj.Namespace)
	if c.clusterWide {
		scope = CronJobs
	}
	if err := c.db.Update(ctx, func(w *store.Writer) error {
		had, err := w.HasTemplateImages(ctx, uid)
		if err != nil {
			return err
		}
		origin := store.OriginObserved
		afterGap := false
		switch {
		case !had && cj.CreationTimestamp.Time.Before(now.Add(-2*time.Minute)):
			origin = store.OriginAdopted
		case had && known && !op.LastSeenAt.IsZero():
			if ok, err := w.WatchedThroughout(ctx, scope, op.LastSeenAt, now); err == nil && !ok {
				origin, afterGap = store.OriginAdopted, true
			}
		}
		return w.SetTemplateImages(ctx, uid, "CronJob", cj.Namespace, cj.Name, cs, origin, now, afterGap)
	}); err != nil {
		return err
	}
	c.images.set(uid, fp)
	return nil
}

// cutUTF8 is s cut to at most n bytes, on a character boundary.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
