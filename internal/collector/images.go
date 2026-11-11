package collector

import (
	"context"
	"encoding/json"
	"sync"
	"time"

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
			img, name := c.Image, c.Name
			if len(img) > maxImageRef {
				img = img[:maxImageRef]
			}
			if len(name) > maxContainerName {
				name = name[:maxContainerName]
			}
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
	if err := c.db.Update(ctx, func(w *store.Writer) error {
		had, err := w.HasTemplateImages(ctx, uid)
		if err != nil {
			return err
		}
		origin := store.OriginObserved
		if !had && cj.CreationTimestamp.Time.Before(now.Add(-2*time.Minute)) {
			origin = store.OriginAdopted
		}
		return w.SetTemplateImages(ctx, uid, "CronJob", cj.Namespace, cj.Name, cs, origin, now)
	}); err != nil {
		return err
	}
	c.images.set(uid, fp)
	return nil
}
