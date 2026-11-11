package collector

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	batchv1 "k8s.io/api/batch/v1"

	"github.com/mihnk/opstide/internal/store"
)

// AnnotationPrefix is where owners describe their CronJobs.
const AnnotationPrefix = "opstide.mihnk.org/"

// Annotation keys, after AnnotationPrefix.
const (
	annName        = AnnotationPrefix + "name"
	annDescription = AnnotationPrefix + "description"
	annOwner       = AnnotationPrefix + "owner"
	annCriticality = AnnotationPrefix + "criticality"
	annRunbook     = AnnotationPrefix + "runbook"
	annDuration    = AnnotationPrefix + "expected-duration"
)

var criticalities = map[string]bool{"low": true, "medium": true, "high": true, "critical": true}

// describe reads the opstide annotations into op. Values are text: the UI
// escapes them, and a runbook is kept only when it is an https URL. What
// is present but unusable is listed in op.MetadataProblems.
func describe(op *store.Operation, ann map[string]string) {
	text := func(key string, max int) string {
		v := strings.TrimSpace(ann[key])
		if !utf8.ValidString(v) {
			op.MetadataProblems = append(op.MetadataProblems, key+" is not valid UTF-8")
			return ""
		}
		if utf8.RuneCountInString(v) > max {
			op.MetadataProblems = append(op.MetadataProblems, fmt.Sprintf("%s is longer than %d characters; shortened", key, max))
			v = string([]rune(v)[:max])
		}
		return v
	}
	op.DisplayName = text(annName, 120)
	op.Description = text(annDescription, 2000)
	op.Owner = text(annOwner, 120)

	if v := strings.ToLower(text(annCriticality, 20)); v != "" {
		if criticalities[v] {
			op.Criticality = v
		} else {
			op.MetadataProblems = append(op.MetadataProblems, annCriticality+" must be low, medium, high or critical")
		}
	}
	if v := text(annRunbook, 2000); v != "" {
		if u, err := url.Parse(v); err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil {
			op.RunbookURL = u.String()
		} else {
			op.MetadataProblems = append(op.MetadataProblems, annRunbook+" must be an https:// link")
		}
	}
	if v := text(annDuration, 40); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 && d <= 7*24*time.Hour {
			op.ExpectedDuration = &d
		} else {
			op.MetadataProblems = append(op.MetadataProblems, annDuration+" must be a duration such as 4m or 1h30m, up to 168h")
		}
	}
}

func operationFrom(cj *batchv1.CronJob, now time.Time) store.Operation {
	op := store.Operation{
		ID: string(cj.UID), Namespace: cj.Namespace, Name: cj.Name,
		Schedule:          cj.Spec.Schedule,
		ConcurrencyPolicy: string(cj.Spec.ConcurrencyPolicy),
		SuccessfulHistory: 3, FailedHistory: 1,
		CreatedAt: mt(&cj.CreationTimestamp), FirstSeenAt: now, LastSeenAt: now,
	}
	if op.ConcurrencyPolicy == "" {
		op.ConcurrencyPolicy = string(batchv1.AllowConcurrent)
	}
	if cj.Spec.TimeZone != nil {
		op.TimeZone = *cj.Spec.TimeZone
	}
	if cj.Spec.Suspend != nil {
		op.Suspend = *cj.Spec.Suspend
	}
	if s := cj.Spec.StartingDeadlineSeconds; s != nil {
		d := time.Duration(*s) * time.Second
		op.StartingDeadline = &d
	}
	if n := cj.Spec.SuccessfulJobsHistoryLimit; n != nil {
		op.SuccessfulHistory = int(*n)
	}
	if n := cj.Spec.FailedJobsHistoryLimit; n != nil {
		op.FailedHistory = int(*n)
	}
	describe(&op, cj.Annotations)
	return op
}

func label(op store.Operation) string {
	if op.DisplayName != "" {
		return op.DisplayName
	}
	return op.Namespace + "/" + op.Name
}

func (c *Collector) syncCronJob(ctx context.Context, it item) error {
	s := c.scopeFor(it.namespace)
	if s == nil {
		return nil
	}
	cj, err := get[*batchv1.CronJob](s.cronJobs, it.namespace, it.name, it.uid)
	if err != nil {
		return err
	}
	if cj == nil {
		if it.uid == "" {
			return nil // replaced or gone; its delete is queued with the UID
		}
		op, ok, err := c.db.Operation(ctx, string(it.uid))
		if err != nil || !ok || !op.DeletedAt.IsZero() {
			return err
		}
		return c.cronJobGone(ctx, op, c.now(), msTime(it.deletedAt), !it.tombstone)
	}

	if err := c.recordSpecWrites(ctx, "cronjob", "CronJob", cj.Namespace, cj.Name, string(cj.UID), cj.ManagedFields); err != nil {
		return err
	}
	if err := c.recordConfigRefs(ctx, string(cj.UID), "CronJob", cj.Namespace, cj.Name, configRefsOf(cj.Spec.JobTemplate.Spec.Template)); err != nil {
		return err
	}
	now := c.now()
	op := operationFrom(cj, now)
	old, had, err := c.db.Operation(ctx, op.ID)
	if err != nil {
		return err
	}
	if had {
		op.FirstSeenAt = old.FirstSeenAt
	}
	base := store.Event{
		Namespace: cj.Namespace, Kind: "CronJob", ResourceUID: op.ID, ResourceName: cj.Name, OperationID: op.ID,
		ObservedAt: now, Severity: "info", Confidence: store.Observed,
	}
	return c.db.Update(ctx, func(w *store.Writer) error {
		if err := w.UpsertOperation(ctx, op); err != nil {
			return err
		}
		var events []store.Event
		if !had {
			e := base
			e.ID, e.Type, e.OccurredAt = "cronjob.created/"+op.ID, "cronjob.created", op.CreatedAt
			e.Summary = fmt.Sprintf("%s created, schedule %s", label(op), op.Schedule)
			events = append(events, e)
		}
		if had && old.Suspend != op.Suspend {
			e := base
			e.Type, e.Summary = "cronjob.resumed", label(op)+" resumed"
			if op.Suspend {
				e.Type, e.Summary, e.Severity = "cronjob.suspended", label(op)+" suspended", "warning"
			}
			e.ID = fmt.Sprintf("%s/%s/%s", e.Type, op.ID, cj.ResourceVersion)
			events = append(events, e)
		}
		if had && (old.Schedule != op.Schedule || old.TimeZone != op.TimeZone) {
			e := base
			e.Type, e.ID = "cronjob.schedule_changed", fmt.Sprintf("cronjob.schedule_changed/%s/%s", op.ID, cj.ResourceVersion)
			e.Summary = fmt.Sprintf("%s schedule changed from %s to %s", label(op), zoned(old), zoned(op))
			e.Facts = map[string]string{"from": old.Schedule, "fromTimeZone": old.TimeZone, "to": op.Schedule, "toTimeZone": op.TimeZone}
			events = append(events, e)
		}
		for _, e := range events {
			if _, err := w.AddEvent(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

func zoned(op store.Operation) string {
	if op.TimeZone == "" {
		return op.Schedule
	}
	return op.Schedule + " " + op.TimeZone
}

// cronJobGone records that a CronJob no longer exists. seen is false when
// it vanished while OpsTide was not watching, so when is unknown;
// deletedAt is its deletionTimestamp when it had one.
func (c *Collector) cronJobGone(ctx context.Context, op store.Operation, now, deletedAt time.Time, seen bool) error {
	e := store.Event{
		ID: "cronjob.deleted/" + op.ID, Type: "cronjob.deleted", Namespace: op.Namespace, Kind: "CronJob",
		ResourceUID: op.ID, ResourceName: op.Name, OperationID: op.ID, ObservedAt: now, Severity: "info",
		Confidence: store.Observed, Summary: label(op) + " deleted",
	}
	e.OccurredAt = deletedAt
	if !seen {
		e.Confidence = store.Unknown
		e.Summary = label(op) + " is no longer in the cluster; it was removed while OpsTide was not watching"
	}
	return c.db.Update(ctx, func(w *store.Writer) error {
		if err := w.MarkOperationDeleted(ctx, op.ID, now); err != nil {
			return err
		}
		_, err := w.AddEvent(ctx, e)
		return err
	})
}

func msTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.UnixMilli(n).UTC()
}
