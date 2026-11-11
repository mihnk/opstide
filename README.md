# OpsTide

Your cluster's operational memory. OpsTide runs inside a Kubernetes cluster,
watches CronJobs, Jobs and rollouts, and keeps a durable record of what was
expected to happen, what actually ran, and when it could not see.

It answers the morning questions without a VPN or a laptop left open: what
ran overnight, what failed, what is still to come today, and whether the
record has holes.

> Status: early development (v0.1 in progress). Nothing here is released yet.

## Principles

- **Observed, derived or unknown.** Every entry says which it is. A Job's
  `Failed=True` condition is observed; "this run was missed" is derived and
  claimed only when the record was continuous; anything else is *not
  confirmed*.
- **Coverage is part of the data.** When the collector was not watching, the
  gap is recorded and shown. Nothing inside a gap is reported as failed or
  missed.
- **Read-only.** OpsTide needs only `list` and `watch`. It never changes
  the cluster, never requests a Secret's data, logs or exec. (Secret *metadata*
  can be recorded if you turn it on; see [ConfigMaps and Secrets](#configmaps-and-secrets).)
- **Private by default.** One pod, one SQLite database on a volume, nothing
  sent out of the cluster. No AI, Prometheus or Loki required.

## How it is built

One Go process with client-go informers writes to SQLite (WAL) on a
PersistentVolume and serves the web UI and a read API on the same port.
It runs as one replica with one writer; high availability is not promised.

```
CronJob / Job / Pod / Deployment watches ──► collector ──► SQLite (PVC)
                                                              │
                         browser ◄── web UI + /api/v1 ◄───────┘
```

## Describing a CronJob

Owners describe their jobs with annotations in the chart, committed through
GitOps. OpsTide only reads them.

| Annotation | Meaning |
|---|---|
| `opstide.mihnk.org/name` | Friendly name |
| `opstide.mihnk.org/description` | What the job does and what a failure means |
| `opstide.mihnk.org/owner` | Owning team |
| `opstide.mihnk.org/criticality` | `low`, `medium`, `high` or `critical` |
| `opstide.mihnk.org/runbook` | `https://` link to the runbook |
| `opstide.mihnk.org/expected-duration` | Go duration, e.g. `4m` |

## Install

```bash
helm install opstide oci://ghcr.io/mihnk/charts/opstide -n opstide --create-namespace
kubectl -n opstide port-forward svc/opstide 8080:80    # then open http://127.0.0.1:8080
```

(From a checkout: `helm install opstide charts/opstide -n opstide --create-namespace`.)

The first release tag publishes the image and the chart as **private**
GitHub packages. Make both public once (organization → Packages → each
package → settings), or add an image pull secret and log in to the
registry before installing.

The chart installs one pod (`Recreate`, never two writers), a 5 Gi volume
for the SQLite record, and a ServiceAccount that can only `list` and `watch`
(OpsTide reads through informers, so the chart grants nothing else) these kinds:

| API group | Kinds | Why |
|---|---|---|
| `batch` | cronjobs, jobs | scheduled operations and their runs |
| core | pods | Pods of Jobs, Deployments, StatefulSets and DaemonSets |
| core | events | Kubernetes Events, only on a cluster that does not serve `events.k8s.io` |
| `events.k8s.io` | events | Kubernetes Events (evidence) |
| `apps` | deployments, replicasets, statefulsets, daemonsets, controllerrevisions | releases and workloads |
| `autoscaling` | horizontalpodautoscalers | scaling context |
| core (only with `nodes.observe`) | nodes | Node conditions, in a separate ClusterRole; cluster-scoped |
| core (unless `watch.configMaps=false`) | configmaps | who created, wrote or deleted a ConfigMap; values are never kept |
| core (only with `watch.secretMetadata`) | secrets | who created, wrote or deleted a Secret, read as metadata only (see below) |

Pod and ControllerRevision objects are reduced to an allow-listed subset as they
arrive (no environment values, commands or arguments are kept). OpsTide never requests
a Secret's data, logs or exec; by default the chart grants nothing on Secrets at all. The pod runs as a non-root user with a read-only
root filesystem and the default seccomp profile.

Before a new version migrates an existing database, OpsTide copies it next to itself as
`opstide.db.pre-v<N>` (N is the schema it had), once per version. Migrations only go forward,
so that copy is the way back to the earlier version; delete it when you no longer need it. It
needs as much free space as the database.

If OpsTide gives up recording an object after repeated failures, that is not hidden: the
scope's coverage shows a gap over the stretch, a Log entry names the object, System says how
many, and `opstide_collector_dropped_total` counts them. Kubernetes Events and autoscalers are
recorded by their own worker, so a burst of them cannot delay a Job or a Pod.

Useful values (see `charts/opstide/values.yaml` for all):

| Value | What |
|---|---|
| `clusterName` | Name shown in the UI |
| `namespaces: [payments, data]` | Watch only these. The chart creates a Role in each instead of a ClusterRole |
| `nodes.observe` | Record Node conditions and scheduling changes (`false` by default). Nodes are cluster-scoped, so this adds a small ClusterRole with `list` and `watch` on nodes, also for installs limited to some namespaces |
| `watch.configMaps` | Record ConfigMaps being created, written and deleted, and by which field manager (`true` by default). Adds `list` and `watch` on configmaps |
| `watch.secretMetadata` | The same for Secrets, through the metadata-only API (`false` by default). Adds `list` and `watch` on secrets: read [what that grants](#configmaps-and-secrets) first |
| `releases.observationMinutes` | How long after a Deployment rollout completes a release is observed before it can be called healthy: 5, 10 (default), 15 or 30 |
| `retention.days`, `retention.maxEntries`, `retention.maxRoutineEntries` | How much history to keep (30 days, 2,000,000 entries). Routine-volume entries (Pod lifecycle, Event entries, autoscaler entries, Helm's release records, folded ConfigMap and Secret writes) have their own budget (half by default), so a burst of them cannot push out older Job and release history; when a budget rather than age removes records, System and the Explorer say so |
| `persistence.size`, `persistence.storageClass`, `persistence.existingClaim` | Where the record lives |
| `auth.enabled`, `auth.existingSecret` | The token that protects the API and the UI (on by default; see below) |
| `clustrail.url` | Adds "Open in Clustrail" to each CronJob: a URL template with `{cluster}`, `{namespace}`, `{name}` (http, https or `clustrail://`) |
| `serviceMonitor.enabled` | A Prometheus Operator ServiceMonitor for `/metrics` |
| `networkPolicy.enabled` | Only the UI port in; out only DNS and the Kubernetes API's ports. Set `networkPolicy.apiServer.cidrs` to your API server's address to limit the destination too. Not for Cilium with a self-managed control plane (see values.yaml) |
| `ingress.enabled` | Off by default |

Two things to know:

- **OpsTide asks for one shared token.** There are no user accounts: whoever
  holds the token can read the record. Read it from the cluster and paste it
  into the sign-in page, or send it as `Authorization: Bearer <token>`:

  ```bash
  kubectl -n opstide get secret opstide-auth -o jsonpath='{.data.token}' | base64 -d; echo
  ```

  The chart makes a random token on install and keeps it through upgrades
  and uninstalls. With Argo CD or anything else that renders with `helm
  template`, create the Secret yourself (key `token`) and set
  `auth.existingSecret`; otherwise every sync makes a new token. The token (at
  least 16 bytes) is read at start: a change through `helm upgrade` restarts
  the pod by itself, one made to the Secret directly needs `kubectl rollout
  restart`. Either signs everyone out, and a session is also refused by the
  server after 30 days. Wrong tokens are slowed down per address (five, then
  one every 10 s); correct ones never are. A token is not a login system: for people-by-name access put an
  OIDC proxy in front. `auth.enabled=false` turns it off. Network isolation is
  not authorization.
- **Uninstalling keeps the volume**, so the record survives a reinstall.
  Delete the PersistentVolumeClaim yourself to drop it.

If you set `namespaces` in the chart, the collector must watch the same
list (the chart passes it for you). A collector that watches the whole
cluster with namespace-only permissions never becomes ready, and System
shows which watches fail.

To run OpsTide inside the demo cluster instead of on your laptop:
`make demo-install`, then port-forward as above.

## The web UI

Open the collector's address in a browser (in development,
http://127.0.0.1:8080). The first visit in a browser starts with a short
introduction: what OpsTide is, what it is watching in this cluster right now,
and where to start. It stays at `/welcome`, linked from System.

The interface is in English or Turkish: the **TR / EN** button in the header
switches it, and the first visit follows the browser's language. Only the
words OpsTide draws itself are translated; what comes from the cluster
(Kubernetes Event messages, entry summaries, failure reasons) stays as
recorded.

Six pages, all reading the API below and updating live:

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
  marked adopted and never assessed. See [docs/M2.5-WORKLOAD-RELEASES.md](docs/M2.5-WORKLOAD-RELEASES.md).
- **Workloads**: Deployments, StatefulSets and DaemonSets in one list, each with its
  revisions, Pods (with ordinal, revision and the node each reported), history and evidence.
  A StatefulSet stopped at its partition, or waiting under OnDelete, is described as that and
  never as a failure. A DaemonSet's Pods are shown by node. Node conditions and scheduling
  changes can be recorded too (off unless `nodes.observe` is set). See
  [docs/M2.7-WORKLOADS.md](docs/M2.7-WORKLOADS.md).
- **Evidence**: on a release, the Kubernetes Events about its ReplicaSet, its Pods
  and its Deployment, the autoscaler (HPA) context of the workload, and a
  facts-only list of what was recorded. Kubernetes' reason and message are shown
  as Kubernetes reported them; OpsTide does not say why anything happened
  ("Root cause: Not established by OpsTide"). See [docs/M2.6-EVIDENCE.md](docs/M2.6-EVIDENCE.md).
- **Explorer** (it replaced the Log; old `/log` links land here): everything recorded, on one timeline (Log entries and Kubernetes Events once each),
  filterable by text, namespace, severity, run outcome, owner, kind of record, provenance and source, with the filters in the link,
  with provenance (observed, derived, unknown), where OpsTide was and was not watching, a drilldown
  per record, what OpsTide knew of a resource at a past time, a tab-only Investigation Workspace and a
  bounded JSON export. It shows evidence and interprets none of it. See
  [docs/M2.8-EXPLORER.md](docs/M2.8-EXPLORER.md).

  **What changed before this.** A record that marks something going wrong (a failed run, a Pod restart, OOMKill,
  eviction or wait, a release that stalled, failed, became unavailable or had rule findings, a Warning Kubernetes
  Event, a Node condition change) shows *Recorded before this*: every change OpsTide recorded in the 15 minutes, hour
  (the default), 6 or 24 hours before it, newest first, with how long before, the resource, the writer (field
  manager) when known, provenance and how it is related: the same resource, its owner (Pod → ReplicaSet → Deployment,
  Job → CronJob), a ConfigMap or Secret its Pod template references, or the Node the Pod ran on at that time. The
  window's coverage comes with it, so "no changes recorded" with every watch whole is told apart from "no changes
  recorded, but watches had gaps" and from a window before OpsTide first watched. Earlier is not a cause: the list
  says what happened before, never why. A failed run on its CronJob's page and a troubled release link to it.
- **Schedules**: every CronJob with its last 7 days, last run, next
  expected run, owner and criticality.
- **Operation detail**: what a CronJob does and what is expected of it,
  its last 7 days on one timeline each, recent runs, and a run's Job and
  pods.
- **System**: when each watch was watching today, failing watches,
  storage and retention.

Dark and light themes follow the system until you pick one. Times show in
your browser's zone or UTC. The app is built into the binary; fonts and
everything else are served by the collector itself.

## Change ledger

"What changed near here, and by what?" is the Explorer with **Changes only**: records that say something was changed
(a spec written, a revision, a replica target, a schedule or suspension, a cordon). For Deployments, StatefulSets,
DaemonSets and CronJobs, OpsTide takes from `managedFields` the **field manager** that wrote the spec (`argocd-controller`,
`kubectl-edit`, `helm`), the kind of operation and when, and keeps nothing else: no fields, no values. A field manager is
the name a client gave itself, not a person; the API server's audit log holds who. A write already there when OpsTide began
watching is marked as such; one older than `retention.days` is not recorded at all.

## ConfigMaps and Secrets

Many incidents start with a configuration change. OpsTide records when a ConfigMap (and, if you turn it on, a Secret) is
created, deleted or written, with the field manager that wrote it, in the same way as the change ledger above: the
manager's name, the kind of operation and the time from `managedFields`, nothing else. A manager's time moves on *any*
write it makes (Helm, `kubectl apply` and Argo CD relabel objects too), so a write entry says who wrote, never by itself
what changed. What changed is told only by comparing content:

| Entry | Meaning |
|---|---|
| `configmap.created`, `secret.created` | Created while OpsTide was watching, by whom |
| `configmap.data_written` | Written by a manager, and the keys or values differ from OpsTide's previous observation (the added, removed and changed keys are named) |
| `configmap.metadata_written` | Written, and the keys and values are the same as before: labels, annotations or other metadata |
| `configmap.written` | Written, and whether the content changed is not known (no earlier observation to compare), or one of several writes in an interval whose content changed |
| `configmap.data_changed` | The content differs and no dated write by a field manager explains it; who wrote is unknown |
| `secret.written` | Written by a manager. A fact says whether that manager's fields include the Secret's data; OpsTide cannot see, and never says, whether the data changed |
| `configmap.writes_folded`, `secret.writes_folded` | Written again within five minutes of the previous write entry: one entry for the burst, the latest write, with how many updates it stands for |
| `configmap.deleted`, `secret.deleted` | Deleted (or found gone at a relist, recorded as not seen happening) |
| `configmap.helm_release_*`, `secret.helm_release_*` | The same for Helm's release records (below) |

They are in the Explorer group *ConfigMaps and Secrets*; all but the Helm records are in **Changes only** and in the
Overview's count of changes. They are history, kept with the rest, except the routine volume below.

- **ConfigMaps** (`watch.configMaps`, on by default). OpsTide reads them through the normal API, because telling a data
  change from a metadata change needs the content. Every value is replaced, as it arrives, by an HMAC-SHA256 under a key
  OpsTide makes once per install and keeps in its database (never shown by the API or exported), so a hash cannot be
  checked against a guessed value (`LOG_LEVEL=debug`). It keeps the HMAC of the whole content, one per key, and the
  names of the keys; no value is kept, in memory or in the database, and no hash is in a record's ID. Lists are asked
  for in pages of 500 from the API server's storage (not its watch cache, which would send every ConfigMap of the
  cluster in one answer), and each page is reduced before the next is asked for, so at most one page of values is ever
  held; the cost is a heavier read on the API server at each start or relist. If the reduction cannot be installed on a
  watch, that watch is not started and System shows it as not watchable: it never runs with values cached.
- **Secrets** (`watch.secretMetadata`, off by default). OpsTide asks the API server for Secrets' **metadata only**
  (`PartialObjectMetadata`, the metadata client), so Secret data is never requested. Metadata still includes
  annotations, and `kubectl.kubernetes.io/last-applied-configuration` on a Secret applied with kubectl holds the whole
  Secret, data included; `managedFields` name its keys (`f:data: {f:password}`). Both arrive with every list and watch
  event and are dropped the moment they arrive, never cached or kept: of `managedFields` only each manager's name,
  operation, time and whether its fields include data are kept. Key names of Secrets are never kept, and no hash or
  value of a Secret is recorded.

  **What turning it on grants:** Kubernetes RBAC cannot grant "metadata only". `watch.secretMetadata=true` gives the
  ServiceAccount `list` and `watch` on `secrets` (in each watched namespace, or cluster-wide), which is enough to read
  their data; OpsTide does not, because it only makes metadata requests, but the permission is the full one. Leave it off
  if that is more than you want to grant. With it off, OpsTide makes no request about Secrets.

**What is a baseline, and what is left out.** An object that already existed when OpsTide first saw it is a baseline:
neither its creation nor the writes its `managedFields` show from before that are recorded, on the first start or any
later one. Writes OpsTide already handled are remembered in its database, so a restart or a relist does not record
them again, and a write older than `retention.days` is never recorded. A ConfigMap used as a leader-election lock is
renewed without an entry or a database write. When an object is deleted, its last state is recorded first, so a write
waiting to be folded, or an object created and deleted before OpsTide got to it, is not lost.

**Routine volume.** Two kinds of entries are counted against the routine-volume retention budget
(`retention.maxRoutineEntries`) rather than the main one:

- *Helm's release records*: a ConfigMap or Secret named `sh.helm.release.v1.*` **and** labelled `owner=helm` (Helm's
  storage; `HELM_DRIVER=configmap` puts them in ConfigMaps). Each `helm upgrade` creates one and relabels the previous
  one, which says nothing a release does not already show. Their entries are `*.helm_release_created`, `_written` and
  `_deleted`, hidden like other routine records unless asked for, and not in **Changes only**. An object with the name
  but not the label is ordinary configuration.
- *Folded writes* (`*.writes_folded`): an object written again within five minutes of its previous write entry, such as
  the cluster autoscaler's status ConfigMap (about 288 entries a day). They stay visible and in **Changes only**.
  OpsTide does not try to recognise "status" ConfigMaps by their writer's name: that would be a guess.

**Which workloads use them.** From the Pod templates of the Deployments, StatefulSets, DaemonSets and CronJobs it
records, OpsTide notes which ConfigMaps and Secrets each names: volumes (projected ones too), `envFrom`, `env`
`valueFrom` and image pull Secrets. In the Explorer, the record of a config change lists those workloads as a
`REFERENCE` relationship: the template names it, by name. That is not ownership and not a cause; whether the change
reached the workload (a restart, a reload) is a separate record, if any. A workload's History includes the changes to
the ConfigMaps and Secrets it names (`/workloads/{id}/history?config=none` leaves them out).

Each watch is a coverage scope (`configmaps`, `secrets`) like the others: a stretch it was not watching is a gap, and
Secret metadata that is not watched is shown as off, never as calm. System says which of the two are on.

## Upgrading

- **ConfigMaps are watched from the upgrade on.** The `configmaps` watch (on by default) begins when the upgraded pod
  starts, so any time before that shows as *not watched* for ConfigMaps, and ConfigMaps already there are a baseline.
- **`rbac.create=false`**: add `list` and `watch` on `configmaps` to the role you manage, or set `watch.configMaps=false`;
  otherwise System shows the ConfigMap watch as unavailable (and coverage shows the gap) until you do.

## Alerting on a CronJob

`/metrics` has one series per live CronJob (the first 1,000): `opstide_operation_last_success_timestamp_seconds`,
`opstide_operation_last_failure_timestamp_seconds`, `opstide_operation_last_run_timestamp_seconds` and
`opstide_operation_suspended`, labelled `cronjob_namespace` and `cronjob` (not `namespace`, which the Prometheus Operator renames). A series is absent until OpsTide has seen such a run.
OpsTide reports when it last saw a run end; the threshold is yours:

```promql
time() - opstide_operation_last_success_timestamp_seconds{cronjob_namespace="data", cronjob="nightly-backup"} > 26 * 3600
```

A series disappears once the runs it comes from age out of retention, so a CronJob that has not run for longer than the kept history, or a monthly one, has none: alert on `absent(...)` as well, and on the CronJob's schedule rather than on the age alone.  Weigh it with whether OpsTide
was watching: `opstide_watches_failing > 0` and `increase(opstide_collector_dropped_total[1d]) > 0` mean the record may
have missed runs, and the UI shows the gap.

## Try it

`make demo-local` (or `go run ./cmd/opstide -demo`) serves a made-up history from a temporary database: CronJobs
with a failed run and an unwatched hour, a Deployment's three releases, a StatefulSet held at its partition,
a DaemonSet, a Node that was recreated under the same name, an api Pod that restarted shortly after its Deployment's spec and its ConfigMap were written, Kubernetes Events, a collection gap and an object
OpsTide gave up on. It needs no cluster, kubeconfig or Docker, collects nothing, and says so on every page. It is
a snapshot, not a live view; to see real watching use kind below.

## Try it on kind

```bash
make demo-local   # no cluster, no Docker: made-up data in a temporary database, http://127.0.0.1:8080
make demo-up      # kind cluster "opstide-demo" with demo CronJobs (needs Docker and kind)
make demo         # OpsTide against it; open http://127.0.0.1:8080
make demo-rollout # a Deployment rollout to see on the Overview
make demo-down    # delete the cluster
```

The demo CronJobs run every 2–5 minutes, so the screens fill within a
quarter of an hour: payment reconciliation fails every tenth minute,
inventory sync sometimes runs long, one CronJob is suspended and one has
no description. They only sleep and exit. Stop `make demo` for a few
minutes and start it again to see a collection gap.

## Development

```bash
make web                     # build the UI (needs Node 22) into the binary's embed folder
make test
make run                     # your kubeconfig's current context, data in ./data
make run CONTEXT=kind-dev    # another context
```

For UI work, run `npm run dev` in `web/` next to `make run`: Vite serves
the app with hot reload and proxies `/api` to the collector.

`npm test` in `web/` also draws every screen, in English and in Turkish,
against API answers recorded from the demo (`web/src/test/demo-api.json`); a
screen fails if it throws, logs a React error or says it could not load. When
the API changes, record them again with a demo running:

```bash
go run ./cmd/opstide -demo -listen 127.0.0.1:18400 &
cd web && node scripts/record-ui-fixtures.cjs http://127.0.0.1:18400 src/test/demo-api.json
```

Probes: `/healthz` (process), `/readyz` (watches synced and storage
writable). They, the page and its scripts need no token; everything else does
when one is set (`-token-file`, or `OPSTIDE_TOKEN` for a quick try). Listening
on a non-loopback address without one logs a warning.

## Metrics

`/metrics` (Prometheus text format, behind the token) describes OpsTide itself,
not your cluster: `opstide_ready`, `opstide_last_heartbeat_timestamp_seconds`
(far behind now means it is stuck), `opstide_watches_failing`,
`opstide_collector_items_total`, `opstide_collector_failures_total`,
`opstide_collector_queue_length`, `opstide_store_events`,
`opstide_store_bytes`, `opstide_store_wal_bytes`,
`opstide_store_oldest_event_timestamp_seconds`, `opstide_build_info` and
`opstide_http_requests_total` by kind of request and status code. A scrape
config with the token:

```yaml
- job_name: opstide
  authorization: { credentials_file: /etc/prometheus/opstide-token }
  static_configs: [{ targets: ['opstide.opstide.svc:80'] }]
```

With the Operator, `--set serviceMonitor.enabled=true`.

## How big it can get

Measured with `make perf` on synthetic data: 500 CronJobs (50 every five
minutes, 150 hourly, 300 daily), 30 days, 549,000 runs and 1.1 million log
entries in a 460 MB database, on one CPU of a cloud sandbox. Computing an
answer, then asking for the same URL again:

| Request | Computed | Again |
|---|---|---|
| Overview, last 3 hours | 42 ms | 1 ms |
| Overview, today | 140 ms | 1 ms |
| Overview, last 24 hours | 210 ms | 1 ms |
| Overview, last 5 days | 1.0 s | 2 ms |
| Overview, last 7 days | 1.4 s | 2 ms |
| Overview strip, 24 h / 7 d / 30 d | 13 / 62 / 312 ms | 1 ms |
| Schedules | 1.2 s | 1 ms |
| One CronJob | 53 ms | 47 ms |
| Log page (any filter) | 2-17 ms | 2-15 ms |
| System | 95 ms | 68 ms |

The heavy answers are kept for three times what they took to compute (a
quarter of a second at least, 20 seconds at most) and only while the record
has not changed: a request made after a change never gets an answer from
before it. Tabs that ask the same thing share one computation, up to 32 MB
of answers are kept, and a view that takes a second to compute is recomputed
at most once per change, so a cluster that changes every few seconds with a
7-day view open spends up to about half a CPU on it. Peak memory while
computing the 7-day view and Schedules was about 150 MB, inside the chart's 512 Mi limit.
Retention (30 days, 2 million entries by default) is what bounds the record;
a cluster with many more runs a day should shorten it. Run the measurement
yourself with `make perf` (`OPSTIDE_PERF_OPS=1000` for a bigger cluster).

Upgrading from an earlier build adds indexes once (about 4 seconds on 550
thousand runs and 1.1 million entries, needing some scratch space in `/tmp`
and a little more room on the volume while it builds). It is one way: an
older image refuses a database a newer one has migrated, so `helm rollback`
to a build before this one needs the volume restored from a backup.

## Read API

Everything the UI shows comes from a read-only JSON API. Times are
RFC 3339 in UTC; `tz` (an IANA zone) says where a day starts.

| Endpoint | What |
|---|---|
| `GET /api/v1/range?within=3h&tz=Europe/Istanbul` | One window of 5 minutes to 7 days: counts, a lane per CronJob, gaps, grouped exceptions, what is still to come. The window is `within=3h` (ending now; also `90m`, `5d`), `date=2026-11-04` (a calendar day in `tz`), or `from=` and `to=`; with none, today. A lane with more than 120 marks comes as runs per hour. `day` is the same endpoint |
| `GET /api/v1/overview?span=5d&tz=` | The strip above the Overview: runs per hour (per 6 h for 30 days) and failures, when Jobs were not watched, and when OpsTide first watched. `span` is `24h`, `5d`, `7d` or `30d` |
| `GET /api/v1/events?within=48h&from=&to=&namespace=&show=runs,starts,schedules,rollouts,kubernetes,scaling,collection&kind=&reason=&workload=&severity=warning,error&outcome=failed&owner=&q=&cursor=` | The log, newest first, by stable cursor. `severity` is `info`, `warning`, `error`; `outcome` narrows the run entries to `failed`, `succeeded` or `unknown` (with `show`, it must include `runs`; without `show` all groups are returned); `owner` is an exact owner annotation |
| `GET /api/v1/operations?tz=` | Every CronJob with its last 7 days, last run and next expected run |
| `GET /api/v1/operations/{id}?tz=&runs=50&before=` | One CronJob: description, schedule, 7 days, recent runs |
| `GET /api/v1/runs/{id}` | One run with its pods and entries |
| `GET /api/v1/releases`, `/releases/{id}`, `/releases/{id}/events`, `/releases/{id}/pods`, `/releases/{id}/changes` (`/events` takes `include=evidence,scaling`) | Deployment releases, their assessment, timeline, Pods and the comparison with the previous revision (see docs/M2.5-WORKLOAD-RELEASES.md) |
| `GET /api/v1/evidence?within=48h&namespace=&reason=&type=Warning&kind=&uid=&workload=&replicaset=&run=&hpa=&q=&cursor=`, `/evidence/{id}` | Kubernetes Events as recorded evidence: what Kubernetes reported, which API, the times and count it gave (null when it gave none), how each record is linked to resources OpsTide saw (`DIRECT`, `OWNERSHIP`, `SAME_RESOURCE_CONTEXT`, `UNRESOLVED`; `REFERENCE` for a workload whose Pod template names a ConfigMap or Secret), and where it came from (see docs/M2.6-EVIDENCE.md) |
| `GET /api/v1/releases/{id}/evidence`, `/workloads/{id}/scaling` | A release's evidence with how each record relates to it, the facts-only summary, and the autoscaler and replica-target context of its Deployment |
| `GET /api/v1/workloads/{id}/history?config=none` | A workload's history; with the changes to the ConfigMaps and Secrets its Pod template names unless `config=none`, and the list of what it names (`references`) |
| `GET /api/v1/explorer`, `/explorer/filters`, `/explorer/records/{id}`, `/explorer/context`, `/explorer/state`, `GET`+`POST /explorer/export`, `POST /explorer/lookup` | The Operational Evidence Explorer: one timeline of everything recorded with provenance, coverage and relationship classifications, a drilldown per record, state at a past time, and bounded JSON export (see docs/M2.8-EXPLORER.md) |
| `GET /api/v1/explorer/records/{id}/before?window=1h` | The changes recorded in the window before a record (`15m`, `1h` (default), `6h` or `24h`; never more), newest first, on its resource and the resources related to it, each with its relationship class (`DIRECT`, `OWNERSHIP`, `REFERENCE`, `SAME_RESOURCE_CONTEXT`), writer and provenance; the related resources looked at; the coverage of the window per watch; and why an empty list is empty (`no_changes_recorded`, `no_changes_recorded_with_gaps`, `before_first_observation`). Before, not because |
| `GET /api/v1/workloads`, `/workloads/{id}`, `/workloads/{id}/history`, `/revisions`, `/pods`, `/evidence`, `GET /api/v1/nodes`, `/nodes/{id}`, `/nodes/{id}/history`, `GET /api/v1/runs/{id}/evidence` | Workloads of every kind, their revisions, Pods and evidence; Node context; the Events linked to a run (see docs/M2.7-WORKLOADS.md) |
| `GET /api/v1/owners` | The owners any CronJob has named, deleted ones included (the Explorer's Owner list) |
| `GET /api/v1/system?tz=` | Watches and coverage today, permissions problems, storage |
| `GET /api/v1/stream` | Server-sent events: `change` when the record changes, `ping` every 15 s |

## License

Apache 2.0. See [LICENSE](LICENSE).
