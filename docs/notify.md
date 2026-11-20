# The morning summary

Every weekday at 08:30 (or whenever you set), OpsTide can post what the night looked like to a Slack channel, a
Microsoft Teams channel or a webhook of your own: how many CronJob runs there were and which failed, which CronJobs are
overdue for a success, Pods that restarted or were OOMKilled with the change recorded nearest before them, the watches
that were not whole, and what is still due today. Every line links to its record when `publicURL` is set.

```
Morning summary · prod-eu · since yesterday 18:00
17 runs: 15 succeeded, 2 failed. 12 changes recorded.
• Failed Nightly backup data/nightly-backup, 06:47 · open
• Overdue Nightly backup: should succeed every 26 h, last success 30 h ago · open
• Pod api-7-b2 restarted (OOMKilled), 14:26 · 7 min after a change by kubectl-edit (2 changes in the hour before) · open
Not watched for part of this time: Kubernetes Events. Nothing is said about them then.
Due today: 4 more, next Archive old orders at 07:30.
Read-only record from OpsTide. Times in Europe/Istanbul. · Overview
```

It is the one thing OpsTide sends out of the cluster, and only when you list a destination. A quiet night still gets a
message ("Nothing needs a look"), so silence never has to be read. "7 min after" says what came earlier, not why.

## Setting it up

**System → Integrations → Set up a destination** in the UI walks through it and writes the two things below for you,
with a preview built from your cluster's record. By hand:

1. Put the webhook URL in a Secret, in OpsTide's namespace. It is a secret: anyone holding it can post to the channel.

   ```bash
   kubectl -n opstide create secret generic opstide-notify --from-literal=slack='https://hooks.slack.com/services/…'
   ```

2. Name it in the chart's values and upgrade:

   ```yaml
   notify:
     morningSummary:
       schedule: "30 8 * * 1-5"   # five-field cron, read in timeZone
       timeZone: Europe/Istanbul
       covers: since-18h          # since-18h (18:00 the day before), 12h or 24h
       language: en               # en or tr
       include: [failures, troubles, gaps, due]   # add changes to list every change, not only the count
     destinations:
       - type: slack              # slack, teams or webhook
         name: "#ops-morning"
         urlFrom: { secret: opstide-notify, key: slack }
   ```

3. Send a test message from System → Integrations. It goes from inside the cluster, so it also shows whether the cluster
   can reach the destination.

The configuration is never changed from the UI: with one shared token, a page that could change it would let anyone
holding the token send the cluster's record somewhere else. OpsTide reads the URL from the Secret each time it sends,
so a rotated URL needs no restart; the URL is never logged, shown or returned by the API, and errors leave it out.

### Slack

Create an app with an **incoming webhook** for the channel (Slack: *Create an app → Incoming Webhooks → Add New
Webhook*), and use its `https://hooks.slack.com/services/…` URL.

### Microsoft Teams

Teams' old incoming webhooks (Office 365 connectors) are being retired; use a **Workflows** webhook: in the channel,
*Workflows → Post to a channel when a webhook request is received*, and use the URL it gives. OpsTide sends a message
with one Adaptive Card, which that workflow posts as is.

### A webhook of your own

`type: webhook` POSTs JSON to any http or https URL (PagerDuty, Opsgenie, an internal bot, n8n):

```json
{
  "schema": 1,
  "kind": "opstide.morning_summary",
  "text": "Morning summary · prod-eu · since yesterday 18:00\n17 runs: …",
  "lines": [{ "kind": "heading", "parts": [{ "text": "Morning summary · …", "bold": true }] }],
  "summary": { "cluster": "prod-eu", "from": "…", "to": "…", "runs": { "total": 17, "failed": 2 }, "failed": […],
               "overdue": […], "troubles": […], "notWatched": […], "due": { "count": 4 } }
}
```

`text` is the message as plain text, `lines` the same with what to show bold and where links go, and `summary` the
data (`GET /api/v1/morning` returns the same shape). A field may be added in a later version; `schema` changes only when
one changes meaning.

## How it is sent

- **On time, or late once.** If OpsTide was not running at the summary's time (an upgrade, a node drain), it sends it
  when it starts, up to 3 hours late, marked *Sent late*. Later than that it waits for the next one. A summary is never
  sent twice for the same time.
- **Retries.** A network error, a 429 or a 5xx answer is tried again twice (after 5 and 20 seconds); another 4xx is
  not. How each delivery went (time, HTTP status, error) shows on System → Integrations and in `/metrics`.
- **What it reads.** The same record as the UI, with the same rules: an overdue CronJob only when OpsTide watched long
  enough to know, and a watch that was not whole is said, never left out.

## Network

With `networkPolicy.enabled`, the chart lets OpsTide out to port 443 anywhere when destinations are listed
(`notify.allowEgress443`), since their addresses are not known in advance. To narrow it, set that to `false` and add
your own rule to `networkPolicy.extraEgress`. Behind an egress proxy, set `HTTPS_PROXY` (and `NO_PROXY`) on the
Deployment: OpsTide honours them.

## Metrics

`opstide_notify_deliveries_total{destination, result}` (ok or failed, tests included),
`opstide_notify_last_success_timestamp_seconds{destination}` and `opstide_notify_next_timestamp_seconds`. To be told
when the summary stops arriving:

```promql
time() - opstide_notify_last_success_timestamp_seconds > 26 * 3600
```
