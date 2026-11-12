# Grafana annotations

Grafana can draw what OpsTide recorded on any dashboard: changes (a spec written, a schedule changed, a ConfigMap
written, a replica target changed: the Explorer's *Changes only* set), failures (failed runs, OOMKilled and restarted
Pods, stalled or failed releases, Warning Kubernetes Events) and the stretches OpsTide was **not watching**, as
regions, so a quiet part of a graph is not read as a calm one. Grafana pulls them from
`GET /api/v1/annotations`; OpsTide sends nothing out. An annotation marks when something was recorded, not why a
graph moved.

It needs a JSON data source, not an OpsTide plugin. With the **Infinity** data source
(`yesoreyeram-infinity-datasource`, Grafana 10, 11 and 12; the old SimpleJSON plugin is not needed):

1. Install Infinity (`grafana cli plugins install yesoreyeram-infinity-datasource`, or the Grafana chart's
   `plugins: [yesoreyeram-infinity-datasource]`).
2. **Connections → Data sources → Add → Infinity**, named `OpsTide`. Under **Authentication** pick **Bearer Token**
   and paste OpsTide's token (the one from `opstide-auth`, see [Install](install.md#the-token-and-uninstalling)); under **Allowed hosts** add
   `http://opstide.opstide.svc` (Infinity sends the token only there). Save.
3. On a dashboard: **Settings → Annotations → New annotation query**, data source `OpsTide`, then in the query:
   - Type **JSON**, Parser **Backend**, Source **URL**, Format **Table**, Method **GET**
   - URL `http://opstide.opstide.svc/api/v1/annotations?from=${__from}&to=${__to}&kinds=changes,failures,gaps`
     (`${__from}` and `${__to}` are the dashboard's range in epoch milliseconds)
   - Rows/Root: empty (the answer is a plain array)
   - Columns: `time` as *Timestamp (epoch ms)*, `timeEnd` as *Timestamp (epoch ms)*, `title`, `text` and `tagList`
     (as `tags`) as *String*
4. Under **Field mappings** map Time → `time`, End time → `timeEnd`, Title → `title`, Text → `text`, Tags → `tags`.
   Save the dashboard.

One query per kind (`kinds=failures`, `kinds=changes`, `kinds=gaps`) gives each its own colour and switch. Add
`&namespace=payments`, or `&workload=<workload id>` (the id in a Workloads page's address), to narrow changes and
failures. Grafana outside the cluster uses OpsTide's ingress address instead; with `networkPolicy.enabled`, list
Grafana's namespace in `networkPolicy.ingressFrom`. Set `publicURL` so each annotation's link opens OpsTide.

The same annotation as dashboard JSON (replace the data source uid with yours):

```json
{
  "name": "OpsTide failures", "enable": true, "iconColor": "red",
  "datasource": { "type": "yesoreyeram-infinity-datasource", "uid": "opstide" },
  "target": {
    "refId": "A", "type": "json", "parser": "backend", "source": "url", "format": "table",
    "url": "http://opstide.opstide.svc/api/v1/annotations?from=${__from}&to=${__to}&kinds=failures,gaps",
    "url_options": { "method": "GET" }, "root_selector": "",
    "columns": [
      { "selector": "time", "text": "time", "type": "timestamp_epoch" },
      { "selector": "timeEnd", "text": "timeEnd", "type": "timestamp_epoch" },
      { "selector": "title", "text": "title", "type": "string" },
      { "selector": "text", "text": "text", "type": "string" },
      { "selector": "tagList", "text": "tags", "type": "string" }
    ]
  },
  "mappings": {
    "time": { "source": "field", "value": "time" }, "timeEnd": { "source": "field", "value": "timeEnd" },
    "title": { "source": "field", "value": "title" }, "text": { "source": "field", "value": "text" },
    "tags": { "source": "field", "value": "tags" }
  }
}
```

Each element of the answer is `{time, timeEnd, title, text, tags, tagList, kind, recordId, provenance, namespace, url}`,
sorted by time: `time` and `timeEnd` are epoch milliseconds, `timeEnd` only on regions; `kind` is `change`, `failure`,
`gap`, `maintenance` (a configured [maintenance window](maintenance-windows.md), a region) or `notice`; `recordId` and `provenance` (`OBSERVED`, `DERIVED`, `UNKNOWN`) are the Explorer record's; `url`
opens the record (or the stretch) in the Explorer. `from` and `to` take epoch milliseconds or RFC 3339 (the last 24
hours when both are left out) and span at most 31 days. `limit` (default 500, at most 1,000) keeps the newest changes
and failures; when there were more, a `notice` region covers the part of the range whose records were left out.
Gaps are the watches the drawn records come from (every watch with `kinds=gaps` alone) that were not watched, merged
into one region per stretch with the watches named, plus the parts of the range before OpsTide first watched or
older than it keeps. A watch that is turned off (Nodes, Secret metadata) is a setting, shown on System, and is not
drawn. More than 500 not-watched stretches in one answer are not drawn one by one: past the 499th a `notice` region covers
the rest of the range. A Warning Event that keeps repeating (a crash loop's `BackOff`) is one record and is drawn once,
at its first occurrence; its later repeats are not drawn, and a range that starts after its first occurrence does not
show it. `from` and `to` below 10¹¹ are refused as epoch seconds (send milliseconds). The request needs the token like
the rest of the API.
