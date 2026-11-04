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
- **Read-only.** OpsTide needs `get`, `list` and `watch`. It never changes
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

## Development

```bash
make test
make run                     # your kubeconfig's current context, data in ./data
make run CONTEXT=kind-dev    # another context
```

Probes: `/healthz` (process), `/readyz` (watches synced and storage
writable).

## Read API

Everything the UI shows comes from a read-only JSON API. Times are
RFC 3339 in UTC; `tz` (an IANA zone) says where a day starts.

| Endpoint | What |
|---|---|
| `GET /api/v1/day?date=2026-11-04&tz=Europe/Istanbul` | One day: counts, a lane per CronJob, gaps, exceptions, what is still to come |
| `GET /api/v1/events?within=48h&from=&to=&namespace=&show=runs,starts,schedules,rollouts,collection&q=&cursor=` | The log, newest first, by stable cursor |
| `GET /api/v1/operations?tz=` | Every CronJob with its last 7 days, last run and next expected run |
| `GET /api/v1/operations/{id}?tz=&runs=50&before=` | One CronJob: description, schedule, 7 days, recent runs |
| `GET /api/v1/runs/{id}` | One run with its pods and entries |
| `GET /api/v1/system?tz=` | Watches and coverage today, permissions problems, storage |
| `GET /api/v1/stream` | Server-sent events: `change` when the record changes, `ping` every 15 s |

## License

Apache 2.0. See [LICENSE](LICENSE).
