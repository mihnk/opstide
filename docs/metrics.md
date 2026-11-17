# Metrics and alerting

`/metrics` (Prometheus text format, behind the token) describes OpsTide itself,
not your cluster: `opstide_ready`, `opstide_last_heartbeat_timestamp_seconds`
(far behind now means it is stuck), `opstide_watches_failing`,
`opstide_collector_items_total`, `opstide_collector_failures_total`,
`opstide_collector_queue_length`, `opstide_store_events`,
`opstide_store_bytes`, `opstide_store_wal_bytes`, `opstide_store_volume_bytes` and
`opstide_store_volume_free_bytes` (the filesystem the database is on, and the space left on it; on Linux and macOS),
`opstide_store_oldest_event_timestamp_seconds`, `opstide_build_info`,
`opstide_maintenance_active{name,scope}` (1 while a configured [maintenance window](maintenance-windows.md) is open somewhere
in its scope, `cluster` or its namespaces; one series per window) and `opstide_http_requests_total` by kind of request and status code. A scrape
config with the token:

```yaml
- job_name: opstide
  authorization: { credentials_file: /etc/prometheus/opstide-token }
  static_configs: [{ targets: ['opstide.opstide.svc:80'] }]
```

With the Operator, `--set serviceMonitor.enabled=true`.

## Alerting on a CronJob

`/metrics` has one series per live CronJob (the first 1,000): `opstide_operation_last_success_timestamp_seconds`,
`opstide_operation_last_failure_timestamp_seconds`, `opstide_operation_last_run_timestamp_seconds` and
`opstide_operation_suspended`, labelled `cronjob_namespace` and `cronjob` (not `namespace`, which the Prometheus Operator renames). A series is absent until OpsTide has seen such a run.
OpsTide reports when it last saw a run end; the threshold is yours:

```promql
time() - opstide_operation_last_success_timestamp_seconds{cronjob_namespace="data", cronjob="nightly-backup"} > 26 * 3600
```

A CronJob that declares `opstide.mihnk.org/must-succeed-within` (see [Success deadlines](ui.md#success-deadlines)) also
has `opstide_operation_succeed_within_seconds` (its limit) and `opstide_operation_overdue`: 1 when OpsTide watched the
whole of the limit and saw no run succeed, 0 when it is on time, and absent while OpsTide cannot tell or the CronJob is
suspended. That alert weighs coverage for you:

```promql
opstide_operation_overdue == 1
```

and this one finds the CronJobs OpsTide cannot judge right now:

```promql
opstide_operation_succeed_within_seconds unless on(cronjob_namespace, cronjob) opstide_operation_overdue
```

A series disappears once the runs it comes from age out of retention, so a CronJob that has not run for longer than the kept history, or a monthly one, has none: alert on `absent(...)` as well, and on the CronJob's schedule rather than on the age alone.  Weigh it with whether OpsTide
was watching: `opstide_watches_failing > 0` and `increase(opstide_collector_dropped_total[1d]) > 0` mean the record may
have missed runs, and the UI shows the gap.

## Alerts about OpsTide itself

`--set prometheusRule.enabled=true` (with `serviceMonitor.enabled`, and `prometheusRule.labels` matching your
Prometheus's rule selector) installs six alerts about OpsTide's own health. None of them is about your workloads:
those are your other alerts' job, and `opstide_operation_overdue` above. Each links here.

### OpsTideDown

Prometheus has no healthy scrape of OpsTide for 5 minutes. Nothing is recorded while it is down; the stretch shows as
*not watched* everywhere once it is back. Look at the pod (`kubectl -n opstide get pods`, its events and last log). A
pod that cannot start after an upgrade usually says why in its log: a configuration error names the setting, and a
database from a newer release is refused (roll forward, or restore the `opstide.db.pre-v<N>` copy).

### OpsTideNotReady

OpsTide runs but a watch has not synced, or storage is not writable, for 10 minutes. System in the UI lists the
watches and what each says. If storage is the problem, see OpsTideVolumeFilling.

### OpsTideStuck

The heartbeat OpsTide writes every 15 seconds is more than 2 minutes old: the process answers but no longer writes its
record. Restart the pod (`kubectl -n opstide rollout restart deployment/opstide`) and keep its log from before: that
is a bug worth reporting.

### OpsTideWatchesFailing

One or more scopes cannot be watched for 15 minutes, nearly always a missing permission: a namespace added to
`namespaces` without a Role there, `rbac.create=false` without the verbs, or a Node or Secret watch turned on without
its ClusterRole. System names each scope and the error. Nothing is recorded for them until it is fixed.

### OpsTideDroppedRecords

OpsTide gave up writing some objects after repeated failures in the last 30 minutes. Each is a gap in coverage, named
in the Explorer with the error that stopped it, so nothing is silently missing.

### OpsTideVolumeFilling

Less than `prometheusRule.volumeFreeRatio` (15%) of the volume is free for 30 minutes. When it runs out, recording
stops. Grow the PersistentVolumeClaim if your storage class allows it, or keep less (lower `retention.days` or
`retention.maxEntries`) so the database stops growing: SQLite reuses the space a prune frees, but the file itself does
not shrink.
