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
  the cluster, never reads Secrets, logs or exec.
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

Pod and ControllerRevision objects are reduced to an allow-listed subset as they
arrive (no environment values, commands or arguments are kept). The chart does not read
Secrets, logs or exec. The pod runs as a non-root user with a read-only
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
| `releases.observationMinutes` | How long after a Deployment rollout completes a release is observed before it can be called healthy: 5, 10 (default), 15 or 30 |
| `retention.days`, `retention.maxEntries` | How much history to keep (30 days, 2,000,000 entries) |
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
http://127.0.0.1:8080). Six pages, all reading the API below and updating
live:

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

## Try it

`make demo-local` (or `go run ./cmd/opstide -demo`) serves a made-up history from a temporary database: CronJobs
with a failed run and an unwatched hour, a Deployment's three releases, a StatefulSet held at its partition,
a DaemonSet, a Node that was recreated under the same name, Kubernetes Events, a collection gap and an object
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
| `GET /api/v1/evidence?within=48h&namespace=&reason=&type=Warning&kind=&uid=&workload=&replicaset=&run=&hpa=&q=&cursor=`, `/evidence/{id}` | Kubernetes Events as recorded evidence: what Kubernetes reported, which API, the times and count it gave (null when it gave none), how each record is linked to resources OpsTide saw (`DIRECT`, `OWNERSHIP`, `SAME_RESOURCE_CONTEXT`, `UNRESOLVED`), and where it came from (see docs/M2.6-EVIDENCE.md) |
| `GET /api/v1/releases/{id}/evidence`, `/workloads/{id}/scaling` | A release's evidence with how each record relates to it, the facts-only summary, and the autoscaler and replica-target context of its Deployment |
| `GET /api/v1/explorer`, `/explorer/filters`, `/explorer/records/{id}`, `/explorer/context`, `/explorer/state`, `GET`+`POST /explorer/export`, `POST /explorer/lookup` | The Operational Evidence Explorer: one timeline of everything recorded with provenance, coverage and relationship classifications, a drilldown per record, state at a past time, and bounded JSON export (see docs/M2.8-EXPLORER.md) |
| `GET /api/v1/workloads`, `/workloads/{id}`, `/workloads/{id}/history`, `/revisions`, `/pods`, `/evidence`, `GET /api/v1/nodes`, `/nodes/{id}`, `/nodes/{id}/history`, `GET /api/v1/runs/{id}/evidence` | Workloads of every kind, their revisions, Pods and evidence; Node context; the Events linked to a run (see docs/M2.7-WORKLOADS.md) |
| `GET /api/v1/owners` | The owners any CronJob has named, deleted ones included (the Explorer's Owner list) |
| `GET /api/v1/system?tz=` | Watches and coverage today, permissions problems, storage |
| `GET /api/v1/stream` | Server-sent events: `change` when the record changes, `ping` every 15 s |

## License

Apache 2.0. See [LICENSE](LICENSE).
