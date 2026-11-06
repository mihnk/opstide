// Types for the Explorer API (/api/v1/explorer) and the words the UI uses for what it returns.
// OpsTide records evidence; nothing here interprets it.

export type Provenance = 'OBSERVED' | 'DERIVED' | 'UNKNOWN'
export type Classification = 'DIRECT' | 'OWNERSHIP' | 'SAME_RESOURCE_CONTEXT' | 'UNRESOLVED'

export interface ResourceRef { kind: string; namespace?: string; name?: string; uid?: string }

export interface ExplorerRecord {
  id: string
  type: string
  group: string
  source: string
  provenance: Provenance
  routine?: boolean
  severity?: string
  summary: string
  reason?: string
  eventType?: string
  resource: ResourceRef
  at: string
  occurredAt: string | null
  lastOccurredAt?: string | null
  observedAt: string
  count?: { value: number; basis: string }
  refs: { workloadId?: string; releaseId?: string; runId?: string; operationId?: string; nodeId?: string; replicaSetUid?: string; hpaUid?: string }
  message?: string
  messageTruncated?: boolean
}

export type EmptyKind = 'no_matches' | 'source_unavailable' | 'outside_retention' | 'before_first_observation' | 'collection_gap'
export interface ExplorerEmpty { kind: EmptyKind; message: string; detail: string[] }

export interface ExplorerList {
  schema: number
  from: string
  to: string
  range: string
  order: 'asc' | 'desc'
  orderBasis: string
  records: ExplorerRecord[]
  nextCursor?: string
  suppressedRoutine: number
  empty?: ExplorerEmpty
  notices: string[]
  provenance: { eventsApi: string; notes: string[] }
}

export interface FilterChoice { value: string; label?: string; count: number }
export interface ExplorerFilters {
  schema: number
  from: string
  to: string
  quickRanges: string[]
  groups: FilterChoice[]
  kinds: FilterChoice[]
  provenance: FilterChoice[]
  sources: string[]
  reasons: FilterChoice[]
  namespaces: FilterChoice[]
  total: number
  counted: string
}

export interface Span { from: string; to: string }
export interface CoverageScope {
  scope: string
  kind: string
  label: string
  namespace?: string
  state: 'watching' | 'gap' | 'unavailable' | 'disabled' | 'before_first_observation'
  watching: Span[]
  gaps: Span[]
  firstWatchedAt: string | null
  lastObservedAt: string | null
  problem?: string
  note?: string
}
export interface ExplorerContext {
  schema: number
  from: string
  to: string
  now: string
  firstObservedAt: string | null
  oldestRecordAt: string | null
  lastWriteAt: string | null
  retention: { days: number; maxEntries: number; boundaryAt: string | null }
  bands: { from: string; to: string; kind: 'outside_retention' | 'before_first_observation' | 'observable' }[]
  scopes: CoverageScope[]
  sources: { name: string; state: string; note?: string }[]
  nodesObserved: boolean
}

export interface Relationship {
  kind: string
  uid?: string
  name?: string
  classification: Classification
  via?: string
  resolved?: boolean
  path?: string
  note?: string
}

export interface ExplorerDetail {
  schema: number
  record: ExplorerRecord
  observation: {
    recordTime: string
    timeBasis: 'occurredAt' | 'firstOccurrence' | 'observedAt'
    explanation: string
    occurredAt: string | null
    lastOccurredAt?: string | null
    observedAt: string
    recordedAt?: string | null
    updatedAt?: string | null
    origin?: string
  }
  provenance: { class: Provenance; source: string; explanation: string }
  derivation: null | {
    ruleId: string
    ruleSet: string
    ruleVersion: string
    inputs: Record<string, string>
    evidenceRefs: { kind: string; id: string; path?: string }[]
    note?: string
  }
  relationships: Relationship[]
  original: { kind: 'log_entry' | 'kubernetes_event'; entry?: { facts?: Record<string, string>; summary: string; severity: string; confidence: string; type: string }; event?: { message: string; reporter?: string; action?: string; source: string; sourceUid: string; origin: string; count: { value: number; basis: string } | null } }
  coverage: { state: string; scopes: string[]; note: string }
  current: null | { label: string; path: string }
  limitations: string[]
}

export interface ExplorerState {
  schema: number
  uid: string
  kind?: string
  at: string
  basis: 'OBSERVED_AT' | 'TRANSITION' | 'LATEST_RECORD' | 'UNKNOWN'
  statement: string
  transition: ExplorerRecord | null
  latestRecord: ExplorerRecord | null
  recent: ExplorerRecord[]
  coverage: { state: string; scopes: string[]; note: string }
  current: null | { label: string; path: string }
}

export interface ExplorerSummary {
  schema: number
  from: string
  to: string
  items: { key: string; label: string; plural: string; count: number; query: string }[]
  notWhole: { label: string; state: string }[]
  dropped: number
}

export interface ExplorerLookup { schema: number; records: ExplorerRecord[]; missing: string[] }

export const emptyTitle: Record<EmptyKind, string> = {
  no_matches: 'No matches',
  source_unavailable: 'Source unavailable',
  outside_retention: 'Outside retained history',
  before_first_observation: 'Before first observation',
  collection_gap: 'Collection gap',
}

export const classWords: Record<Classification, string> = {
  DIRECT: 'direct (named by UID)',
  OWNERSHIP: 'ownership (ownerReferences)',
  SAME_RESOURCE_CONTEXT: 'same resource context',
  UNRESOLVED: 'unresolved',
}

export const groupWords: Record<string, string> = {
  operations: 'Scheduled operations', releases: 'Deployment releases', workloads: 'StatefulSets and DaemonSets', pods: 'Pod lifecycle',
  nodes: 'Node conditions', scaling: 'Scaling', observation: 'Observation', events: 'Kubernetes Events', other: 'Other',
}

/** The filters the page keeps in its link. Nothing sensitive: no token, no Event message. */
export const filterKeys = ['range', 'from', 'to', 'q', 'namespace', 'kind', 'name', 'uid', 'workload', 'release', 'run', 'operation', 'node', 'group', 'type', 'reason', 'source', 'provenance', 'severity', 'outcome', 'owner', 'routine'] as const

export const quickRanges = ['15m', '1h', '6h', '24h', '7d'] as const

/** The Explorer link for some filters (a drilldown from another page). */
export function explorerLink(f: Partial<Record<(typeof filterKeys)[number] | 'rec', string>>): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(f)) if (v) p.set(k, v)
  const s = p.toString()
  return `/explorer${s ? `?${s}` : ''}`
}

/** An Explorer range that holds [from, to] with an hour of room either side; to defaults to now. */
export function rangeAround(from: string, to?: string): { from: string; to: string } {
  const a = Date.parse(from) - 3600e3
  const b = (to ? Date.parse(to) : Date.now()) + 3600e3
  const cap = 399 * 86400e3
  return { from: new Date(Math.max(a, b - cap)).toISOString(), to: new Date(b).toISOString() }
}

/** The record id of a Log entry as the Explorer knows it. A Kubernetes Event's Log entry stands for the Event record. */
export function recordIdOf(e: { recordId: string; type: string; facts?: Record<string, string> }): string {
  if (e.type.startsWith('evidence.') && e.facts?.evidenceId) return `k.${e.facts.evidenceId}`
  return e.recordId
}

// The Log is the Explorer with its old filters: a link to /log still works, and lands here.
const logGroups: Record<string, string[]> = {
  runs: ['operations'], starts: ['operations'], schedules: ['operations'], rollouts: ['releases'], releases: ['releases'], health: ['releases'], pods: ['pods'],
  kubernetes: ['events'], scaling: ['scaling'], statefulsets: ['workloads'], daemonsets: ['workloads'], nodes: ['nodes'], collection: ['observation'],
}

/** The Explorer link for what a link to the removed Log screen asked for. */
export function logToExplorer(p: URLSearchParams, now = Date.now()): string {
  const out: Partial<Record<(typeof filterKeys)[number], string>> = {}
  for (const k of ['q', 'namespace', 'severity', 'outcome', 'owner', 'release', 'reason', 'kind', 'workload'] as const) {
    const v = p.get(k)
    if (v) out[k] = v
  }
  const range = p.get('range') ?? '48h'
  if (range === '24h' || range === '7d') out.range = range
  else {
    const hours = range === '30d' ? 720 : 48
    out.from = new Date(now - hours * 3600e3).toISOString().replace(/\.\d{3}Z$/, 'Z')
    out.to = new Date(now).toISOString().replace(/\.\d{3}Z$/, 'Z')
  }
  const show = p.get('show')
  if (show) {
    const parts = show.split(',')
    const groups = [...new Set(parts.flatMap((s) => logGroups[s] ?? []))]
    if (groups.length > 0 && groups.length < 7) out.group = groups.join(',')
    if (parts.includes('counts') || parts.includes('starts') || parts.includes('pods')) out.routine = '1'
  }
  return explorerLink(out)
}

/** The word shown for a record's type: the part after the last dot, readable. */
export function typeWord(t: string): string {
  if (t === 'kubernetes.event') return 'Kubernetes Event'
  return t.replace(/\./g, ' · ').replace(/_/g, ' ')
}
