// Types and fetch for the collector's read API (/api/v1). Times are
// RFC 3339 strings in UTC.

export type MarkState =
  | 'succeeded' | 'failed' | 'running' | 'pending' | 'unknown'
  | 'expected' | 'due' | 'missed' | 'unconfirmed' | 'skipped' | 'suspended'

export interface Span { from: string; to: string }

export interface Mark {
  start: string
  end?: string
  state: MarkState
  runId?: string
  count?: number
  slow?: boolean
  manual?: boolean
  why?: string
}

export interface Pod {
  name: string
  uid: string
  phase: string
  reason?: string
  exitCode?: number
  startedAt?: string
  finishedAt?: string
}

export interface Run {
  id: string
  operationId?: string
  namespace: string
  job: string
  scheduledAt?: string
  basis?: 'annotation' | 'name' | 'manual'
  createdAt: string
  startedAt?: string
  finishedAt?: string
  status: 'pending' | 'running' | 'succeeded' | 'failed' | 'unknown'
  reason?: string
  seconds?: number
  slow?: boolean
  attempts: number
  pods?: Pod[]
}

export interface Operation {
  id: string
  namespace: string
  name: string
  displayName?: string
  description?: string
  owner?: string
  criticality?: string
  runbookUrl?: string
  expectedSeconds?: number
  metadataProblems?: string[]
  schedule: string
  timeZone?: string
  zoneAssumed?: string
  scheduleError?: string
  suspended?: boolean
  concurrency: string
  startingDeadlineSeconds?: number
  successfulJobsHistoryLimit: number
  failedJobsHistoryLimit: number
  createdAt: string
  firstSeenAt: string
  deletedAt?: string
}

export interface Bucket { start: string; runs: number; failed?: number; unknown?: number; missed?: number; skipped?: number }

export interface Lane {
  operationId: string
  name: string
  namespace: string
  schedule: string
  timeZone?: string
  suspended?: boolean
  expectedSeconds?: number
  scheduleError?: string
  mode: 'runs' | 'hourly'
  marks: Mark[]
  buckets?: Bucket[]
  truncated?: boolean
}

export interface Exception {
  kind: 'failing' | 'failed' | 'lost' | 'slow' | 'missed' | 'skipped' | 'gap'
  at: string
  from?: string
  to?: string
  count: number
  operationId?: string
  name?: string
  runId?: string
  scheduledAt?: string
  seconds?: number
  expectedSeconds?: number
  attempts?: number
  reason?: string
  owner?: string
  runbookUrl?: string
  why?: string
  operations?: number
  expectedRuns?: number
}

export interface Range {
  schema: number
  cluster: string
  timeZone: string
  from: string
  to: string
  now: string
  lastWrite?: string
  date?: string
  bucketSeconds: number
  watchedSince?: string
  counts: {
    observed: number; succeeded: number; failed: number; running: number; unknown: number
    unconfirmed: number; missed: number; skipped: number; stillDue: number
  }
  gaps: Span[]
  lanes: Lane[]
  idle: number
  rollouts: { at: string; namespace: string; name: string; revision: string; summary: string }[]
  exceptions: Exception[]
  upcoming: { at: string; operationId: string; name: string; namespace: string }[]
}

export interface Overview {
  schema: number
  span: string
  from: string
  to: string
  now: string
  watchedSince?: string
  gaps: Span[]
  bucketSeconds: number
  buckets: { start: string; runs: number; failed?: number }[]
  retentionDays: number
}

export type CellState = 'none' | 'suspended' | 'ok' | 'unconfirmed' | 'missed' | 'failed'

export interface OperationRow extends Operation {
  week: { date: string; state: CellState; runs: number }[]
  lastRun?: Run
  next?: string
  attention?: string[]
}

export interface Operations {
  schema: number
  timeZone: string
  now: string
  days: string[]
  operations: OperationRow[]
}

export interface OperationDetail {
  schema: number
  clustrailUrl?: string
  timeZone: string
  now: string
  operation: Operation
  next?: string
  timesToday: number
  suspensions: Span[]
  days: { date: string; from: string; to: string; marks: Mark[] }[]
  gaps: Span[]
  runs: Run[]
  moreRuns: boolean
}

export interface Entry {
  id: string
  recordId: string
  type: string
  namespace?: string
  kind?: string
  resourceName?: string
  resourceUid?: string
  reason?: string
  workloadId?: string
  operationId?: string
  runId?: string
  releaseId?: string
  at: string
  occurredAt?: string
  observedAt: string
  recordedAt: string
  severity: 'info' | 'warning' | 'error'
  confidence: 'observed' | 'derived' | 'unknown'
  summary: string
  facts?: Record<string, string>
}

export interface Entries { schema: number; entries: Entry[]; nextCursor?: string; truncated?: boolean }

/** A ConfigMap or Secret a workload's Pod template names, by name. */
export interface ConfigRef { kind: 'ConfigMap' | 'Secret'; name: string; firstSeenAt: string; endedAt: string | null }
export interface WorkloadHistory extends Entries { references: ConfigRef[] }

export interface RunDetail { schema: number; run: Run; operation?: Operation; entries: Entry[] }

export interface System {
  schema: number
  version: string
  cluster: string
  startedAt: string
  now: string
  lastWrite?: string
  synced: boolean
  namespaces: string[]
  timeZone: string
  from: string
  scopes: { scope: string; kind: string; namespace?: string; watching: Span[]; problem?: string }[]
  storage: {
    schemaVersion: number; bytes: number; walBytes: number; entries: number; oldestEntry?: string
    lastPrune?: string; lastPruneRemoved: number; lastCheckpoint?: string; retentionDays: number; maxEntries: number
    sources: { name: string; label: string; retainedSince: string | null; cappedAt: string | null; cappedTotal: number }[]
  }
  integrations: { name: string; state: string; note: string }[]
  workload: WorkloadSystem
  evidence: EvidenceSystem
  demo: boolean
  dropped: { count: number; last: string | null }
  workloads: {
    counts: Record<string, number>
    nodes: { enabled: boolean; live: number; recorded: number; lastObservedAt: string | null; note: string }
    degraded: string[]
  }
  config: { configMaps: boolean; secretMetadata: boolean; configMapCount: number; secretCount: number; configMapNote: string; secretNote: string; degraded: string[] }
}

export interface EvidenceSystem {
  source: string
  records: number
  warnings: number
  latestObserved?: string
  autoscalers: number
  degraded: string[]
}

export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

export async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const res = await fetch(path, { signal, headers: { Accept: 'application/json' } })
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) msg = body.error
    } catch {
      // not JSON; keep the status line
    }
    // The session ended or the token changed: let the app ask for it again.
    if (res.status === 401) window.dispatchEvent(new Event(UNAUTHORIZED))
    throw new ApiError(res.status, msg)
  }
  return (await res.json()) as T
}

/** POSTs a small JSON body. The endpoints that take one only read; nothing is written. */
export async function postJSON<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const res = await postRaw(path, body, signal)
  return (await res.json()) as T
}

async function postRaw(path: string, body: unknown, signal?: AbortSignal): Promise<Response> {
  const res = await fetch(path, { method: 'POST', signal, headers: { Accept: 'application/json', 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`
    try {
      const b = (await res.json()) as { error?: string }
      if (b.error) msg = b.error
    } catch {
      // not JSON; keep the status line
    }
    if (res.status === 401) window.dispatchEvent(new Event(UNAUTHORIZED))
    throw new ApiError(res.status, msg)
  }
  return res
}

/** POSTs a body and hands the response to the person as a file download. Used for the export of selected records. */
export async function downloadPost(path: string, body: unknown, fallbackName: string): Promise<void> {
  const res = await postRaw(path, body)
  const blob = await res.blob()
  const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') ?? '')?.[1] ?? fallbackName
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

/** Fired on the window when the API answers 401. */
export const UNAUTHORIZED = 'opstide:unauthorized'

export function query(params: Record<string, string | number | undefined | null>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== '') q.set(k, String(v))
  }
  const s = q.toString()
  return s ? `?${s}` : ''
}

// ---- Releases: Deployment revisions as OpsTide observed them ----

export type RolloutState = 'observed' | 'progressing' | 'completed' | 'stalled' | 'failed' | 'superseded' | 'unknown'
export type HealthState = 'still_observing' | 'healthy' | 'degraded' | 'insufficient_evidence'

export interface Brief { at: string; type: string; severity: 'info' | 'warning' | 'error'; summary: string }

export interface ReleaseRow {
  id: string
  namespace: string
  workload: string
  revision: string
  origin: 'observed' | 'adopted'
  observedAt: string
  startedAt?: string
  completedAt?: string
  supersededAt?: string
  state: RolloutState
  stateReason?: string
  health: HealthState
  healthReasons: string[]
  desired: number
  updated: number
  ready: number
  available: number
  paused?: boolean
  observeUntil?: string
  windowMinutes: number
  prevId?: string
  recent: Brief[]
}

export interface Releases {
  schema: number
  now: string
  releases: ReleaseRow[]
  nextCursor?: string
  windowMinutes: number
  warnings: string[]
}

export interface Finding { kind: string; at?: string; observed?: string; pod?: string; container?: string; summary: string }

export interface Assessment {
  health: HealthState
  reasons: string[]
  findings: Finding[]
  windowStart?: string
  windowEnd?: string
  evidence?: string[]
  windowMinutes: number
  windows: number[]
  stored: boolean
  /** Observations set aside from the verdict, each with the rule that did so. */
  context?: { rule: string; summary: string; inputs?: Record<string, string> }[]
  /** The rule set that produced the verdict: 1 is the original, 2 sets scaling convergence apart. */
  version?: number
}

export interface Probe { kind: string; path?: string; port?: string; scheme?: string; commandHash?: string; initialDelaySeconds?: number; periodSeconds?: number }
export interface SnapContainer {
  name: string
  image: string
  requests?: Record<string, string>
  limits?: Record<string, string>
  readiness?: Probe
  liveness?: Probe
  startup?: Probe
  envNames?: string[]
}
export interface Snapshot {
  containers: SnapContainer[]
  initContainers?: SnapContainer[]
  configMaps?: string[]
  secrets?: string[]
  serviceAccount?: string
  deployment?: { replicas?: number; strategyType?: string; maxSurge?: string; maxUnavailable?: string }
}

export interface PrevRef { id: string; revision: string; origin: string; observedAt: string }

export interface ReleaseDetail {
  schema: number
  now: string
  release: ReleaseRow
  workload: { uid: string; namespace: string; name: string; deletedAt?: string }
  replicaSet: { uid: string; name: string; templateHash?: string; fingerprint: string }
  assessment: Assessment
  previous?: PrevRef
  snapshot: Snapshot
  imageIds: Record<string, string>
  warnings: string[]
  /** The newest record saying this release stalled, failed, became unavailable or had rule findings. */
  troubleRecordId?: string
}

export interface PodContainer { name: string; init?: boolean; restarts: number; lastReason?: string; lastExit?: number; oomKilled: number; waiting: string[] }
export interface PodView {
  uid: string
  name: string
  node?: string
  state: 'pending' | 'running' | 'ready' | 'not ready' | 'terminating' | 'terminated'
  createdAt: string
  scheduledAt?: string
  readyAt?: string
  terminatedAt?: string
  termReason?: string
  evicted?: boolean
  restarts: number
  containers: PodContainer[]
  readiness: { at: string; ready: boolean }[]
  quiet?: boolean
}
export interface ReleasePods {
  schema: number
  replicaSet: { uid: string; name: string; templateHash?: string; fingerprint: string }
  pods: PodView[]
  truncated: boolean
}

export interface ChangeView { kind: 'changed' | 'added' | 'removed'; field: string; container?: string; before?: string; after?: string; summary: string }
export interface ReleaseChanges {
  schema: number
  available: boolean
  reason?: string
  previous?: PrevRef
  changes: ChangeView[]
  sameTemplate: boolean
  noCapturedDifferences: boolean
  deploymentCompared: boolean
}

export interface WorkloadSystem { windowMinutes: number; activeWindows: number; lastEvent?: string; degraded: string[] }

/** How two records are connected. A classification of the link, never a cause. */
export type Relationship = 'DIRECT' | 'OWNERSHIP' | 'SAME_RESOURCE_CONTEXT' | 'UNRESOLVED'

export interface EvidenceLink { kind: string; uid?: string; relationship: Relationship; via?: string }

export interface EvidenceRecord {
  id: string
  recordId: string
  source: string
  kind: 'KubernetesEvent'
  sourceUid: string
  namespace: string
  name: string
  reason: string
  eventType: string
  message: string
  messageTruncated?: boolean
  action?: string
  reporter?: string
  occurredAt: { first: string | null; last: string | null }
  observedAt: string
  updatedAt: string
  count: { value: number; basis: string } | null
  resource: { kind: string; namespace?: string; name?: string; uid?: string }
  related?: { kind: string; namespace?: string; name?: string; uid?: string }
  relationship: Relationship
  links: EvidenceLink[]
  origin: 'observed' | 'adopted'
  sourceDeletedAt: string | null
  certainty: 'OBSERVED'
}

export interface ReleaseRecord extends EvidenceRecord {
  releaseRelationship: Relationship
  releaseVia: string
  scope: 'release' | 'workload'
}

export interface SummaryFact { text: string; rule: string; at?: string; refs: string[] }

export interface ReleaseEvidence {
  schema: number
  release: string
  from: string
  to?: string
  records: ReleaseRecord[]
  scaling: Entry[]
  summary: { facts: SummaryFact[]; rootCause: string; complete: boolean; note?: string }
  context: { rule: string; summary: string; inputs?: Record<string, string> }[]
  truncated: boolean
  counts: Record<string, number>
  provenance: { eventsApi: string; notes: string[] }
}

export interface HPAView {
  uid: string
  namespace: string
  name: string
  target: string
  targetRelationship: Relationship
  targetVia: string
  minReplicas: number | null
  maxReplicas: number
  currentReplicas: number
  desiredReplicas: number
  lastScaleTime: string | null
  conditions: { type: string; status: string; reason?: string; message?: string; transition?: string }[]
  metrics: { type: string; name?: string; current?: string; target?: string }[]
  updatedAt: string
  deletedAt: string | null
}

export interface WorkloadScaling {
  schema: number
  workload: { uid: string; namespace: string; name: string }
  replicaTarget: number | null
  hpas: HPAView[]
  transitions: Entry[]
  records: EvidenceRecord[]
}

// ---- Workloads and Nodes (M2.7) ----

export interface WorkloadState {
  generation: number
  observedGeneration: number
  desired: number
  ready: number
  available: number
  updated: number
  current: number
  misscheduled?: number
  revision: string
  updateRevision: string
  strategy: string
  partition: number | null
  maxUnavailable?: string
  maxSurge?: string
  podManagement?: string
  minReadySeconds: number
  rollout: string
  rolloutNote: string
  conditions: { type: string; status: string; reason?: string; message?: string; transition?: string }[]
  lastChangeAt: string
  changeBasis: 'observed' | 'baseline'
}

export type WorkloadKind = 'Deployment' | 'StatefulSet' | 'DaemonSet'

export interface WorkloadView {
  id: string
  kind: WorkloadKind
  namespace: string
  name: string
  origin?: string
  status: 'live' | 'deleted'
  firstSeenAt: string
  lastSeenAt: string
  deletedAt: string | null
  state: WorkloadState | null
  evidence: { warnings24h: number; latestWarningReason?: string; latestWarningAt?: string }
  sections?: string[]
}

export interface WorkloadList { schema: number; workloads: WorkloadView[]; nextCursor?: string; notes: string[] }
export interface WorkloadDetail { schema: number; workload: WorkloadView; release?: { id: string; revision: string }; notes: string[] }

export interface RevisionView {
  id: string
  source: 'ControllerRevision' | 'Release'
  name: string
  number: number
  createdAt?: string
  observedAt: string
  origin: string
  isCurrent: boolean
  isUpdate: boolean
  images: string[]
  fingerprint: string
  releaseId?: string
  comparison: { available: boolean; reason?: string; changes: { kind: string; field: string; container?: string; before?: string; after?: string }[] }
}
export interface WorkloadRevisions { schema: number; kind: WorkloadKind; revisions: RevisionView[]; notes: string[] }

export interface WorkloadPodView extends PodView {
  ordinal?: number
  revision?: string
  revisionRole?: 'update' | 'current' | 'other'
  nodeId?: string
}
export interface WorkloadPods {
  schema: number
  kind: WorkloadKind
  pods: WorkloadPodView[]
  nodesWithoutPod: { nodeId: string; name: string }[] | null
  notes: string[]
  truncated?: boolean
}
export interface WorkloadEvidence { schema: number; records: EvidenceRecord[]; nextCursor?: string; conditions: WorkloadState['conditions']; provenance: { eventsApi: string; notes: string[] } }

export interface NodeCondition { type: string; status: string; reason?: string; message?: string; lastTransitionTime: string | null; observedAt: string | null }
export interface NodeView {
  id: string
  name: string
  status: 'live' | 'removed' | 'not_observed'
  lastObservedAt: string
  origin: string
  createdAt: string | null
  firstSeenAt: string
  deletedAt: string | null
  unschedulable: boolean
  conditions: NodeCondition[]
}
export interface NodesList { schema: number; enabled: boolean; nodes: NodeView[]; notes: string[] }
export interface NodeDetail {
  schema: number
  node: NodeView
  pods: { uid: string; name: string; namespace: string; workloadId?: string; workloadKind?: string; createdAt: string; terminatedAt: string | null }[]
  notes: string[]
  podsTruncated?: boolean
}
export interface NodeHistory { schema: number; entries: Entry[]; nextCursor?: string; records: EvidenceRecord[] }
export interface RunEvidence { schema: number; records: EvidenceRecord[]; notes: string[]; provenance: { eventsApi: string; notes: string[] } }
