# Metrics and alerting

`/metrics` (Prometheus text format, behind the token) describes OpsTide itself,
not your cluster: `opstide_ready`, `opstide_last_heartbeat_timestamp_seconds`
(far behind now means it is stuck), `opstide_watches_failing`,
`opstide_collector_items_total`, `opstide_collector_failures_total`,
`opstide_collector_queue_length`, `opstide_store_events`,
`opstide_store_bytes`, `opstide_store_wal_bytes`,
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

A series disappears once the runs it comes from age out of retention, so a CronJob that has not run for longer than the kept history, or a monthly one, has none: alert on `absent(...)` as well, and on the CronJob's schedule rather than on the age alone.  Weigh it with whether OpsTide
was watching: `opstide_watches_failing > 0` and `increase(opstide_collector_dropped_total[1d]) > 0` mean the record may
have missed runs, and the UI shows the gap.
