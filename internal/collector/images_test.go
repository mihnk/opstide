package collector

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mihnk/opstide/internal/store"
)

// A CronJob's job template images are recorded with their containers (init containers marked): a CronJob already there
// is adopted; a new image for a container ends the old row and opens a new one; deleting the CronJob ends the rest.
func TestCronJobTemplateImagesAreRecorded(t *testing.T) {
	db := openStore(t)
	cj := cronJob("data", "backup", "cj-img", nil)
	cj.Spec.JobTemplate = batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "wait", Image: "busybox:1.36"}},
		Containers:     []corev1.Container{{Name: "dump", Image: "postgres:16.4", Command: []string{"secret-command"}}},
	}}}}
	client, ctx := start(t, db, Options{}, cj)
	read := func() []store.TemplateImage {
		ti, err := db.TemplateImages(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		return ti
	}
	eventually(t, "template images", func() bool { return len(read()) == 2 })
	for _, ti := range read() {
		if ti.Origin != store.OriginAdopted || !ti.EndedAt.IsZero() || ti.WorkloadKind != "CronJob" || ti.Namespace != "data" || ti.WorkloadName != "backup" {
			t.Fatalf("%+v", ti)
		}
		if ti.Container == "wait" && (!ti.Init || ti.Image != "busybox:1.36") || ti.Container == "dump" && (ti.Init || ti.Image != "postgres:16.4") {
			t.Fatalf("%+v", ti)
		}
	}
	cj = cj.DeepCopy()
	cj.ResourceVersion = "2"
	cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image = "postgres:16.5"
	if _, err := client.BatchV1().CronJobs("data").Update(ctx, cj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new image", func() bool { return len(read()) == 3 })
	var ended, opened int
	for _, ti := range read() {
		switch {
		case ti.Image == "postgres:16.4" && !ti.EndedAt.IsZero() && ti.EndReason == store.TemplateEndReplaced:
			ended++
		case ti.Image == "postgres:16.5" && ti.EndedAt.IsZero() && ti.Origin == store.OriginObserved:
			opened++
		}
	}
	if ended != 1 || opened != 1 {
		t.Fatalf("%+v", read())
	}
	if err := client.BatchV1().CronJobs("data").Delete(ctx, "backup", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ended with the CronJob", func() bool {
		for _, ti := range read() {
			if ti.EndedAt.IsZero() {
				return false
			}
		}
		return true
	})
	for _, ti := range read() {
		if ti.Image != "postgres:16.4" && ti.EndReason != store.TemplateEndWorkloadDeleted {
			t.Fatalf("%+v", ti)
		}
	}
}

// An image reference longer than OpsTide stores is cut on a character boundary: still valid UTF-8.
func TestTemplateImagesAreCutOnACharacterBoundary(t *testing.T) {
	long := strings.Repeat("a", maxImageRef-1) + "ü:1"
	cs := templateContainers(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: long}}}})
	if len(cs) != 1 || !utf8.ValidString(cs[0].Image) || len(cs[0].Image) != maxImageRef-1 {
		t.Fatalf("%d %q", len(cs[0].Image), cs[0].Image[len(cs[0].Image)-4:])
	}
}

// A template change found after OpsTide was not watching the CronJobs (a gap since it last saw this one): the old row
// ends with reason unknown and the new one opens as adopted, not as a change seen when it happened.
func TestCronJobTemplateChangeAfterAGapIsNotDated(t *testing.T) {
	db := openStore(t)
	cj := cronJob("data", "backup", "cj-gap", nil)
	cj.Spec.JobTemplate = batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "dump", Image: "postgres:16.4"}},
	}}}}
	client, ctx := start(t, db, Options{}, cj)
	read := func() []store.TemplateImage {
		ti, err := db.TemplateImages(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		return ti
	}
	eventually(t, "template images", func() bool { return len(read()) == 1 })
	op, ok, err := db.Operation(ctx, "cj-gap")
	eventually(t, "the operation", func() bool { op, ok, err = db.Operation(ctx, "cj-gap"); return ok && err == nil })
	// A gap in the CronJob watch after it was last seen.
	gapFrom := op.LastSeenAt.Add(time.Millisecond)
	if err := db.Update(ctx, func(w *store.Writer) error {
		for _, sc := range []string{"cronjobs", "cronjobs/data"} {
			if err := w.CloseInterval(ctx, sc, gapFrom, "test gap"); err != nil {
				return err
			}
		}
		for _, sc := range []string{"cronjobs", "cronjobs/data"} {
			if err := w.OpenInterval(ctx, sc, store.Healthy, gapFrom.Add(10*time.Millisecond)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	cj = cj.DeepCopy()
	cj.ResourceVersion = "2"
	cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image = "postgres:16.5"
	if _, err := client.BatchV1().CronJobs("data").Update(ctx, cj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new image", func() bool { return len(read()) == 2 })
	for _, ti := range read() {
		switch ti.Image {
		case "postgres:16.4":
			if ti.EndReason != store.TemplateEndUnknown {
				t.Fatalf("%+v", ti)
			}
		case "postgres:16.5":
			if ti.Origin != store.OriginAdopted {
				t.Fatalf("%+v", ti)
			}
		}
	}
}
