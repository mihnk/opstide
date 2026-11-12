# How big it can get

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
| Schedules (with the run-time trend over 30 days) | 1.4 s | 1 ms |
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
