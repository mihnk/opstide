# Maintenance windows

Planned work (a Saturday 02:00–04:00 node upgrade, a database migration) makes missed runs and restarts that are not
incidents. Declare it in the Helm values, and OpsTide marks what falls inside it, so it reads as planned:

```yaml
maintenance:
  windows:
    - name: Saturday node upgrade
      cron: "0 2 * * 6"          # five fields, read in timeZone (UTC when empty), as the CronJob controller reads them
      duration: 2h
      timeZone: Europe/Istanbul
      note: Node pool upgrade by the platform team
    - name: orders-db migration
      start: "2026-11-21T22:00:00Z"   # one-off: RFC 3339
      end: "2026-11-22T01:00:00Z"
      namespaces: [data]             # left out or []: the whole cluster, cluster-scoped records (Nodes) included
```

The chart renders them into a ConfigMap the binary reads at start (`-maintenance-file`, `OPSTIDE_MAINTENANCE_FILE`) and
restarts the pod when they change. They are never edited in the UI or the API. Each is checked strictly when OpsTide
starts: an unknown field, a time or cron that does not parse, a cron that never fires, a one-off longer than 31 days or a
recurring duration outside 1 minute to 168 hours, a recurring duration longer than the time between two of its starts
(occurrences would overlap: `* * * * *` takes at most `1m`), a namespace that is not a namespace name, a name used
twice or more than 100 windows, and OpsTide refuses to start with the window named in the error. A recurring window's
occurrences follow daylight saving in its zone, unlike a CronJob: a start at a wall time the clocks skip (`30 2 * * *`
on the spring-forward night in America/New_York) opens the window at the first instant that exists after it (03:00),
and a start in a repeated hour (`30 1 * * *` on the fall-back night) opens it once, at the first 01:30. One question
expands at most 1,000 occurrences of a window and 10,000 in all; when a range holds more, the answer says the list is
incomplete (`maintenanceTruncated` on the Overview, `maintenanceWindowsTruncated` in *Recorded before this*, a
`notice` region in Grafana), and records are still marked correctly. System flags a window that names a namespace
OpsTide does not watch.

**Nothing is hidden.** A record whose time falls inside a window that covers its namespace gets the window as context:
`maintenance: {name, from, to, provenance: DERIVED, statement: "inside maintenance window <name>"}` on the API's records,
never written into the stored record. A Kubernetes Event about a cluster-scoped object (a Node, a Namespace, a
PersistentVolume, a ClusterRole …) is stored by Kubernetes in `default` but matched as cluster-scoped: only a
cluster-wide window covers it. Where it shows:

- **Overview**: windows are a tinted band on the lanes they cover (not the grey hatch of "not watching"); an exception
  inside one is still listed, still red when it is a failure, labelled *during maintenance <name>* and after the
  others. A grouped exception (a failure streak, several failed runs) is labelled only when every run in it falls in
  the same occurrence; otherwise the runs inside and outside are separate entries (a streak stays one, unlabelled).
  Missed runs are grouped per occurrence. A stretch OpsTide was not watching is never labelled or moved down.
- **Schedules**: under *Needs a look*, a CronJob that needs a look only for what happened during maintenance is
  labelled with the window and listed after the others, its reasons still in red; a run not confirmed because OpsTide
  was not watching is never put down to maintenance.
- **Explorer**: a chip on each record inside a window, the window in the record's details and in *Recorded before this*,
  and a filter *Outside maintenance only* (off by default; `maintenance=outside`) that leaves them out of the list,
  not out of the record, and says how many it left out (`suppressedMaintenance`).
- **Grafana annotations**: `kinds=maintenance` gives one region per occurrence (in the default kinds); a change or
  failure inside one is tagged `maintenance:<name>`.
- **System** lists the windows and their next occurrences, and `/metrics` has `opstide_maintenance_active{name,scope}`,
  1 while a window is open somewhere in its scope (`scope` is `cluster`, or the window's namespaces sorted and joined
  with commas: a namespaced window being open says nothing about other namespaces), so an alert rule can hold back
  during it:

  ```promql
  ... unless on() opstide_maintenance_active{name="Saturday node upgrade"} == 1
  ```

A window says work was planned then, as declared. It is not evidence of what the work did, and not a cause.
