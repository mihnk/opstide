# Image inventory

"What version was running in prod last Tuesday at 10:00?" and "where does image X run?" OpsTide answers both from what
it already records, with no watch of its own:

- **Deployments**: each release's ReplicaSet Pod template, with the image ID a Pod reported when one did;
- **StatefulSets and DaemonSets**: each ControllerRevision's template (Pods held back by a partition, OnDelete or a
  slow rollout can still run an earlier one);
- **CronJobs**: the job template, recorded from the upgrade that added this (schema 12) on (a CronJob's Jobs run it
  when the schedule fires).

Each use carries two provenances: `templateProvenance` is `OBSERVED` (OpsTide saw the template name the image, in a
live object), and `provenance`, of the stretch (since and until), is `DERIVED` (a rule applied to when revisions became
current), or `UNKNOWN` when the revision's rollout failed (its Pods may never have run it) or a time is not known. A
stalled rollout stays `DERIVED`, with a note.

A *use* is one container of one workload running one image: **since** the revision that gave it the image became
current (Kubernetes' creation time of the revision; when OpsTide saw it become current after a rollback; or, for
something already in use when OpsTide first recorded it, that time, marked as such), **until** a newer revision gave the
container another image, removed the container, or the workload was deleted. A rollback OpsTide did not see is dated
when it found it (`first_recorded`), and the use before it ended *by* then (`untilBasis: unknown`); with no usable
time at all, `since` is null (`sinceBasis: unknown`, `sinceAfter` the time it is known to be after), never a copy of
another time. A StatefulSet or DaemonSet rolled back to an earlier ControllerRevision keeps that revision's earlier
stretch (Kubernetes renumbers it in place). A CronJob template row says why it ended (`replaced`,
`container_removed`, `workload_deleted`, schema 13); a change found at the first sync after OpsTide was not watching
opens the new image as `first_recorded` and ends the old one with an unknown time. A use still open in a namespace
OpsTide no longer watches is not called current (`untilBasis: unknown`). Init containers are labelled. An image is
shown as written (`repository[:tag][@digest]`); a missing tag is not filled in, and one as long as OpsTide stores (253
bytes in a revision, 512 in a CronJob template) is flagged `imageTruncated`.

`GET /api/v1/images?at=` answers for the revision each workload had then. When OpsTide was not watching then (a gap,
before it first watched), each use is `UNKNOWN` and the answer says why; a use whose time is not known that may have
been current then is listed as `UNKNOWN`; a workload whose held revisions all start after `at` (and whose first
revision is not among them) is listed under `unknown`, never left out as if it ran nothing. Revisions are kept until
they *stopped* being current longer ago than `retention.days`; older than that, the answer says so (`coverage.state:
outside_retention`) rather than show a guess. Times are those of revisions, not of Pods pulling images. Notes come
both as English (`notes`, `note`) and as codes with values (`noteCodes`, `notes` on each use: `adopted_created`,
`rollout_state`, `controller_revision`, `cronjob_template`, `cronjob_adopted`, `rollback_found`, `since_unknown`,
`earlier_period`, `until_unknown`, `changed_while_unwatched`, `namespace_not_watched`, `image_truncated`, `coverage`,
`cronjob_images_since`, `no_record`, `revision_times`, `uncertain_at`, `workloads_history_starts`), which the UI words
in the reader's language.
