# Install and configure

```bash
helm install opstide oci://ghcr.io/mihnk/charts/opstide -n opstide --create-namespace
kubectl -n opstide port-forward svc/opstide 8080:80    # then open http://127.0.0.1:8080
```

(From a checkout: `helm install opstide charts/opstide -n opstide --create-namespace`.)

The first release tag publishes the image and the chart as **private**
GitHub packages. Make both public once (organization → Packages → each
package → settings), or add an image pull secret and log in to the
registry before installing.

## What the chart installs

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

## Data safety

Before a new version migrates an existing database, OpsTide copies it next to itself as
`opstide.db.pre-v<N>` (N is the schema it had), once per version. Migrations only go forward,
so that copy is the way back to the earlier version; delete it when you no longer need it. It
needs as much free space as the database.

If OpsTide gives up recording an object after repeated failures, that is not hidden: the
scope's coverage shows a gap over the stretch, a Log entry names the object, System says how
many, and `opstide_collector_dropped_total` counts them. Kubernetes Events and autoscalers are
recorded by their own worker, so a burst of them cannot delay a Job or a Pod.

## Chart values

Useful values (see `charts/opstide/values.yaml` for all):

| Value | What |
|---|---|
| `clusterName` | Name shown in the UI |
| `namespaces: [payments, data]` | Watch only these. The chart creates a Role in each instead of a ClusterRole |
| `nodes.observe` | Record Node conditions and scheduling changes (`false` by default). Nodes are cluster-scoped, so this adds a small ClusterRole with `list` and `watch` on nodes, also for installs limited to some namespaces |
| `watch.configMaps` | Record ConfigMaps being created, written and deleted, and by which field manager (`true` by default). Adds `list` and `watch` on configmaps |
| `watch.secretMetadata` | The same for Secrets, through the metadata-only API (`false` by default). Adds `list` and `watch` on secrets: read [what that grants](configuration-changes.md#configmaps-and-secrets) first |
| `releases.observationMinutes` | How long after a Deployment rollout completes a release is observed before it can be called healthy: 5, 10 (default), 15 or 30 |
| `retention.days`, `retention.maxEntries`, `retention.maxRoutineEntries` | How much history to keep (30 days, 2,000,000 entries). Routine-volume entries (Pod lifecycle, Event entries, autoscaler entries, Helm's release records, folded ConfigMap and Secret writes) have their own budget (half by default), so a burst of them cannot push out older Job and release history; when a budget rather than age removes records, System and the Explorer say so |
| `persistence.size`, `persistence.storageClass`, `persistence.existingClaim` | Where the record lives |
| `auth.enabled`, `auth.existingSecret` | The token that protects the API and the UI (on by default; see below) |
| `publicURL` | The address people open OpsTide at (`https://opstide.example.com`), so the links in [Grafana annotations](grafana.md) open it; empty gives paths |
| `maintenance.windows` | Planned work, so what happens in it reads as planned: a list of `{name, cron, duration, timeZone, namespaces, note}` (recurring) or `{name, start, end, namespaces, note}` (one-off). Empty by default; see [Maintenance windows](maintenance-windows.md) |
| `clustrail.url` | Adds "Open in Clustrail" to each CronJob: a URL template with `{cluster}`, `{namespace}`, `{name}` (http, https or `clustrail://`) |
| `serviceMonitor.enabled` | A Prometheus Operator ServiceMonitor for `/metrics` |
| `networkPolicy.enabled` | Only the UI port in; out only DNS and the Kubernetes API's ports. Set `networkPolicy.apiServer.cidrs` to your API server's address to limit the destination too. Not for Cilium with a self-managed control plane (see values.yaml) |
| `ingress.enabled` | Off by default |

## The token, and uninstalling

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

## Upgrading

- **ConfigMaps are watched from the upgrade on.** The `configmaps` watch (on by default) begins when the upgraded pod
  starts, so any time before that shows as *not watched* for ConfigMaps, and ConfigMaps already there are a baseline.
- **CronJob images are recorded from the upgrade on** (schema 12, a new table): the image inventory holds no CronJob
  image from before it and says so.
- **Schema 13** records why a CronJob template image row ended; rows ended before it say `workload_deleted` when the
  CronJob's deletion matches, and otherwise read as replaced (when the next image starts there) or ended at a time not
  known. Release and ControllerRevision history is now kept until a revision stopped being current more than
  `retention.days` ago (before, by when it was first seen), so the database can hold a little more. Deployments', StatefulSets' and DaemonSets' images come from revisions already held.
- **The run-time trend adds an index on runs** (schema 11) when the upgraded pod first starts; like any migration it
  first copies the database to `opstide.db.pre-v10`.
- **`rbac.create=false`**: add `list` and `watch` on `configmaps` to the role you manage, or set `watch.configMaps=false`;
  otherwise System shows the ConfigMap watch as unavailable (and coverage shows the gap) until you do.
