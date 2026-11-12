# The web UI

Open the collector's address in a browser (in development,
http://127.0.0.1:8080). The first visit in a browser starts with a short
introduction: what OpsTide is, what it is watching in this cluster right now,
and where to start. It stays at `/welcome`, linked from System.

The interface is in English or Turkish: the **TR / EN** button in the header
switches it, and the first visit follows the browser's language. Only the
words OpsTide draws itself are translated; what comes from the cluster
(Kubernetes Event messages, entry summaries, failure reasons) stays as
recorded.

Seven pages, all reading the API below and updating live:

- **Overview**: the last 24 hours across everything (counts of what was recorded, each a link, and the watches that were not whole), then a window of time as a timetable, one lane per CronJob. Pick
  Today, Yesterday, the last 1 h to 5 d, a day or a custom window, or drag
  the window on the strip above it, which shows runs per hour over the last
  24 h, 5, 7 or 30 days with failures in red. Bar width is a run's
  duration; a lane with too many runs to draw one by one shows runs per
  hour. Time when OpsTide was not watching is one flat band across the
  lanes, never a claim: nothing before OpsTide first watched is judged.
  Exceptions are grouped (a streak of failures is one line, a gap is one
  line however many runs fell in it) and each has a Show button that moves
  the window to it. The window is in the link.
- **Releases**: every Deployment revision OpsTide saw appear: whether the
  rollout completed, stalled or failed, what OpsTide's rules found in the
  minutes after (no findings, rule findings, or not enough evidence), what changed from the previous revision, and its Pods' restarts,
  OOMKills and readiness. A revision that already existed when OpsTide started is
  marked adopted and never assessed. See [docs/M2.5-WORKLOAD-RELEASES.md](M2.5-WORKLOAD-RELEASES.md).
- **Workloads**: Deployments, StatefulSets and DaemonSets in one list, each with its
  revisions, Pods (with ordinal, revision and the node each reported), history and evidence.
  A StatefulSet stopped at its partition, or waiting under OnDelete, is described as that and
  never as a failure. A DaemonSet's Pods are shown by node. Node conditions and scheduling
  changes can be recorded too (off unless `nodes.observe` is set). See
  [docs/M2.7-WORKLOADS.md](M2.7-WORKLOADS.md).
- **Images**: which container images the workloads run, where, since when and until when, now or at a past time;
  one image's history across workloads. See [Image inventory](image-inventory.md).
- **Evidence**: on a release, the Kubernetes Events about its ReplicaSet, its Pods
  and its Deployment, the autoscaler (HPA) context of the workload, and a
  facts-only list of what was recorded. Kubernetes' reason and message are shown
  as Kubernetes reported them; OpsTide does not say why anything happened
  ("Root cause: Not established by OpsTide"). See [docs/M2.6-EVIDENCE.md](M2.6-EVIDENCE.md).
- **Explorer** (it replaced the Log; old `/log` links land here): everything recorded, on one timeline (Log entries and Kubernetes Events once each),
  filterable by text, namespace, severity, run outcome, owner, kind of record, provenance and source, with the filters in the link,
  with provenance (observed, derived, unknown), where OpsTide was and was not watching, a drilldown
  per record, what OpsTide knew of a resource at a past time, a tab-only Investigation Workspace and a
  bounded JSON export. It shows evidence and interprets none of it. See
  [docs/M2.8-EXPLORER.md](M2.8-EXPLORER.md).

  **What changed before this.** A record that marks something going wrong (a failed run, a Pod restart, OOMKill,
  eviction or wait, a release that stalled, failed, became unavailable or had rule findings, a Warning Kubernetes
  Event, a Node condition change) shows *Recorded before this*: every change OpsTide recorded in the 15 minutes, hour
  (the default), 6 or 24 hours before it, newest first, with how long before, the resource, the writer (field
  manager) when known, provenance and how it is related: the same resource, its owner (Pod → ReplicaSet → Deployment,
  Job → CronJob), a ConfigMap or Secret its Pod template references, or the Node the Pod ran on at that time. The
  window's coverage comes with it, so "no changes recorded" with every watch whole is told apart from "no changes
  recorded, but watches had gaps", from a window before OpsTide first watched, from a window part of whose records are
  no longer held (the age limit, or a size budget that removed the oldest), and from a record some of whose related
  resources could not be resolved (an owner or Node OpsTide holds no record of): only the first reads as watched.
  Watches are checked in the namespace of what depends on them, and one turned off is reported off. A ConfigMap or
  Secret counts only while the Pod template named it, and an autoscaler's change is listed as the autoscaler's. Earlier is not a cause: the list
  says what happened before, never why. A failed run on its CronJob's page and a troubled release link to it.
- **Schedules**: every CronJob with its last 7 days, last run, run time
  (`p50 3m 10s · 7d`), next expected run, owner and criticality.
- **Operation detail**: what a CronJob does and what is expected of it,
  its last 7 days on one timeline each, its run time against its baseline,
  recent runs, and a run's Job and pods.

**Run time** compares a CronJob's recent runs with its earlier ones, as numbers, never a verdict. *Recent* is the last
7 days in your time zone (today and the six days before), *baseline* the 23 days before that (shorter when the kept
history is). Only succeeded runs with a recorded start and end count, in the period their start falls in; failed runs,
runs whose outcome is unknown and runs still going do not. For each period OpsTide gives the number of runs and the
p50 and p95 durations (nearest-rank: the duration of the run at rank ⌈p × n⌉, so always one a run actually took), and
the ratio of recent p50 to baseline p50 when both periods have at least 5 runs; otherwise it says there are too few.
A ratio of 1.3 or more (30% longer) with enough runs is marked *longer than its baseline*, with the numbers beside it.
A period in which OpsTide was not watching Jobs (or that the kept history cuts short) is marked *not whole*: the runs
it did not see are not counted, and not counted as absent either. The Operation page adds the durations of the latest
60 succeeded runs as small bars.
- **System**: when each watch was watching today, failing watches,
  storage and retention, and the configured maintenance windows with their next occurrences.

Dark and light themes follow the system until you pick one. Times show in
your browser's zone or UTC. The app is built into the binary; fonts and
everything else are served by the collector itself.
