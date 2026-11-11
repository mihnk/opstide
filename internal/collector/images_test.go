package collector

import (
	"testing"

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
		case ti.Image == "postgres:16.4" && !ti.EndedAt.IsZero():
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
}
