# Change ledger, ConfigMaps and Secrets

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
