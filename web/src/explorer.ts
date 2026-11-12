// Types for the Explorer API (/api/v1/explorer) and the words the UI uses for what it returns.
// OpsTide records evidence; nothing here interprets it.

import { createElement, Fragment, type ReactNode } from 'react'
import { locale, t, tc } from './i18n'
import type { MaintenanceMark, MaintenanceSpan } from './api'

export type Provenance = 'OBSERVED' | 'DERIVED' | 'UNKNOWN'
export type Classification = 'DIRECT' | 'OWNERSHIP' | 'SAME_RESOURCE_CONTEXT' | 'UNRESOLVED' | 'REFERENCE'

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
  /** The configured maintenance window the record's time falls in (DERIVED context; the record is not hidden). */
  maintenance?: MaintenanceMark
  /** A collection.gap record: the stretch it says was not watched, and the watch. */
  gap?: { from: string; to: string; scope?: string }
}

/** 'gap' is what the API sends for a collection gap. */
export type EmptyKind = 'no_matches' | 'source_unavailable' | 'outside_retention' | 'before_first_observation' | 'gap' | 'collection_gap'
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
  /** Records maintenance=outside left out (inside a declared window); 0 without that filter. */
  suppressedMaintenance?: number
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
  /** error, warning and info records; each count leaves out its own filter. */
  severity?: FilterChoice[]
  /** Records of operations, by owner annotation. */
  owners?: FilterChoice[]
  /** Finished runs by outcome: failed, succeeded, unknown. */
  outcomes?: FilterChoice[]
  total: number
  counted: string
}

/** GET /explorer/histogram: records per stretch of the range, by class, with the list's filters. */
export interface ExplorerBucket { start: string; error: number; warning: number; change: number; other: number }
export interface ExplorerHistogram {
  schema: number
  from: string
  to: string
  bucketSeconds: number
  buckets: ExplorerBucket[]
  total: number
  counted: string
  classes: string
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
export interface RetentionSource { name: string; label: string; retainedSince: string | null; cappedAt: string | null; cappedTotal: number }

export interface ExplorerContext {
  schema: number
  from: string
  to: string
  now: string
  firstObservedAt: string | null
  oldestRecordAt: string | null
  lastWriteAt: string | null
  retention: { days: number; maxEntries: number; boundaryAt: string | null; sources: RetentionSource[] }
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
  items: { key: string; label: string; plural: string; note?: string; count: number; query: string }[]
  notWhole: { label: string; state: string }[]
  dropped: number
}

/** The changes OpsTide recorded in a window before a record (GET /explorer/records/{id}/before). Newest first. */
export type BeforeWindow = '15m' | '1h' | '6h' | '24h'
export interface BeforeChange {
  record: ExplorerRecord
  secondsBefore: number
  writer?: string
  classification: Classification
  via: string
  subject: ResourceRef
}
export interface ExplorerBefore {
  schema: number
  record: ExplorerRecord
  failureLike: boolean
  window: BeforeWindow
  from: string
  to: string
  order: 'newest_first'
  orderBasis: string
  changeTypes: string[]
  related: (ResourceRef & { classification: Classification; via: string })[]
  /** More resources were related than are looked at; the rest were not. */
  subjectsCapped: boolean
  changes: BeforeChange[]
  truncated: boolean
  coverage: {
    /** watching only when everything related was resolved, every watch was whole and every record of the window is held. */
    state: 'watching' | 'gap' | 'unresolved' | 'outside_retention' | 'before_first_observation'
    firstObservedAt: string | null
    /** Records before this are no longer held (age limit or size budget); null when the whole window is. */
    heldFrom: string | null
    heldNote?: string
    unresolved: ('unresolved_subject' | 'nothing_watched' | 'subjects_capped')[]
    scopes: { scope: string; label: string; namespace?: string; state: string; gaps: Span[]; note?: string }[]
  }
  empty?: { kind: 'no_changes_recorded' | 'no_changes_recorded_with_gaps' | 'unresolved' | 'outside_retention' | 'before_first_observation'; message: string; detail: string[] }
  note: string
  /** The maintenance window the record falls in, and every occurrence that overlaps the window looked at. */
  maintenance?: MaintenanceMark
  maintenanceWindows?: MaintenanceSpan[]
  maintenanceNote?: string
  /** More occurrences overlap the window than one question expands: maintenanceWindows is incomplete. */
  maintenanceWindowsTruncated?: boolean
}

export const beforeWindows: BeforeWindow[] = ['15m', '1h', '6h', '24h']

/** The record types that say something was changed (the API's change ledger, preset=changes). */
export const changeTypes = new Set([
  'deployment.spec_written', 'statefulset.spec_written', 'daemonset.spec_written', 'cronjob.spec_written',
  'release.observed', 'statefulset.revision_changed', 'daemonset.revision_changed', 'workload.replica_target_changed', 'hpa.desired_replicas_changed',
  'cronjob.schedule_changed', 'cronjob.suspended', 'cronjob.resumed', 'node.unschedulable_changed',
  'configmap.created', 'configmap.data_written', 'configmap.metadata_written', 'configmap.written', 'configmap.data_changed', 'configmap.writes_folded', 'configmap.deleted',
  'secret.created', 'secret.written', 'secret.writes_folded', 'secret.deleted',
])

/** The class a record is drawn in, the same rule as the volume chart: an error, else a warning, else a change, else other. */
export type RecordClass = 'error' | 'warning' | 'change' | 'other'
export function classOf(r: { type: string; severity?: string; eventType?: string }): RecordClass {
  if (r.severity === 'error') return 'error'
  if (r.severity === 'warning' || (r.type === 'kubernetes.event' && r.eventType === 'Warning')) return 'warning'
  return changeTypes.has(r.type) ? 'change' : 'other'
}

const failureTypes = new Set(['job.failed', 'pod.restarted', 'pod.oomkilled', 'pod.evicted', 'pod.waiting', 'release.stalled', 'release.failed', 'release.unavailable', 'release.health.degraded', 'node.condition_changed'])

/** Whether a record marks something going wrong, so "What changed before this" is offered on it (the API answers for any). */
export function isFailureLike(r: { type: string; eventType?: string }): boolean {
  return failureTypes.has(r.type) || (r.type === 'kubernetes.event' && r.eventType === 'Warning')
}

/** "14 min before", in the interface language. */
export function beforeWords(seconds: number): string {
  if (seconds < 60) return t('{n} s before', { n: seconds })
  const m = Math.floor(seconds / 60)
  if (m < 60) return t('{n} min before', { n: m })
  const h = Math.floor(m / 60)
  return m % 60 ? t('{h} h {m} min before', { h, m: m % 60 }) : t('{h} h before', { h })
}

export interface ExplorerLookup { schema: number; records: ExplorerRecord[]; missing: string[] }

// The words below are read at render time (getters), so they follow the chosen language.
export const emptyTitle: Record<EmptyKind, string> = {
  get no_matches() { return t('No matches') },
  get source_unavailable() { return t('Source unavailable') },
  get outside_retention() { return t('Outside retained history') },
  get before_first_observation() { return t('Before first observation') },
  get gap() { return t('Collection gap') },
  get collection_gap() { return t('Collection gap') },
}

export const classWords: Record<Classification, string> = {
  get DIRECT() { return t('direct (named by UID)') },
  get OWNERSHIP() { return t('ownership (ownerReferences)') },
  get SAME_RESOURCE_CONTEXT() { return t('same resource context') },
  get UNRESOLVED() { return t('unresolved') },
  get REFERENCE() { return t('reference (named in its Pod template)') },
}

export const groupWords: Record<string, string> = {
  get operations() { return t('Scheduled operations') },
  get releases() { return t('Deployment releases') },
  get workloads() { return t('StatefulSets and DaemonSets') },
  get pods() { return t('Pod lifecycle') },
  get nodes() { return t('Node conditions') },
  get scaling() { return t('Scaling') },
  get config() { return t('ConfigMaps and Secrets') },
  get observation() { return t('Observation') },
  get events() { return t('Kubernetes Events') },
  get other() { return t('Other') },
}

/** The short words of the record-type facet: what a reader calls each group. Kubernetes kind names stay English. */
export const groupShort: Record<string, string> = {
  get operations() { return t('CronJob runs') },
  get releases() { return tc('facet', 'Deployment releases') },
  get workloads() { return 'StatefulSet, DaemonSet' },
  get config() { return 'ConfigMap, Secret' },
  get events() { return 'Kubernetes Event' },
  get pods() { return 'Pod' },
  get observation() { return t('Watch gaps') },
  get nodes() { return 'Node' },
  get scaling() { return t('Scaling') },
  get other() { return t('Other') },
}
export const groupOrder = ['operations', 'releases', 'workloads', 'config', 'events', 'pods', 'observation', 'nodes', 'scaling']

/** The display word for a provenance class (the value itself is never changed). */
export function provWord(p: string): string {
  return t(p.toLowerCase())
}

/** A translated sentence with parts (links, bold numbers) put in where its {name} placeholders are. */
export function fill(template: string, parts: Record<string, ReactNode>): ReactNode[] {
  return template.split(/(\{\w+\})/).filter((x) => x !== '').map((x, i) => {
    const m = /^\{(\w+)\}$/.exec(x)
    return createElement(Fragment, { key: i }, m && m[1] in parts ? parts[m[1]] : x)
  })
}

/** The filters the page keeps in its link. Nothing sensitive: no token, no Event message. */
export const filterKeys = ['range', 'from', 'to', 'q', 'namespace', 'kind', 'name', 'uid', 'workload', 'release', 'run', 'operation', 'node', 'group', 'type', 'reason', 'source', 'provenance', 'severity', 'outcome', 'owner', 'preset', 'routine'] as const

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
  runs: ['operations'], starts: ['operations'], schedules: ['operations'], rollouts: ['releases'], releases: ['releases'], health: ['releases', 'pods'], pods: ['pods'],
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
    if (parts.includes('counts') && !groups.includes('workloads')) groups.push('workloads')
    if (groups.length > 0 && groups.length < 7) out.group = groups.join(',')
    if (parts.includes('counts') || parts.includes('starts') || parts.includes('pods')) out.routine = '1'
    // The old Log applied an outcome to run entries only, and only while they were shown.
    if (!parts.includes('runs')) delete out.outcome
  }
  return explorerLink(out)
}

/** The word shown for a record's type: the part after the last dot, readable. */
export function typeWord(t: string): string {
  if (t === 'kubernetes.event') return 'Kubernetes Event'
  return t.replace(/\./g, ' · ').replace(/_/g, ' ')
}

// ---- filter tokens ----
// Every active filter is shown as a token in the search field: a key and a value, removable. A person can also type one
// (key:value, then Enter). Keys and enumerated values are accepted in English as sent to the API and in the interface
// language; what is sent is always the API's own value.

export interface TokenKey {
  param: string
  /** Several values, sent comma-separated. */
  multi: boolean
  /** The values this key takes, when it takes only some; any value otherwise. */
  values?: () => { value: string; word: string }[]
}

const enumWords = {
  outcome: () => [{ value: 'failed', word: tc('token', 'failed') }, { value: 'succeeded', word: tc('token', 'succeeded') }, { value: 'unknown', word: tc('token', 'unknown') }],
  provenance: () => ['observed', 'derived', 'unknown'].map((v) => ({ value: v, word: provWord(v) })),
  severity: () => [{ value: 'error', word: t('error') }, { value: 'warning', word: t('warning') }, { value: 'info', word: tc('token', 'info') }],
  group: () => groupOrder.map((g) => ({ value: g, word: groupShort[g] })),
  preset: () => [{ value: 'changes', word: tc('token', 'changes') }],
  maintenance: () => [{ value: 'outside', word: tc('token', 'outside') }],
  source: () => ['kubernetes.api', 'opstide.rule', 'opstide.coverage', 'kubernetes.events.k8s.io/v1', 'kubernetes.core/v1'].map((v) => ({ value: v, word: v })),
}

export const tokenKeys: TokenKey[] = [
  { param: 'namespace', multi: false }, { param: 'kind', multi: false }, { param: 'name', multi: false }, { param: 'uid', multi: false },
  { param: 'workload', multi: false }, { param: 'release', multi: false }, { param: 'run', multi: false }, { param: 'operation', multi: false },
  { param: 'node', multi: false }, { param: 'group', multi: true, values: enumWords.group }, { param: 'type', multi: true }, { param: 'reason', multi: false },
  { param: 'source', multi: true, values: enumWords.source }, { param: 'provenance', multi: true, values: enumWords.provenance },
  { param: 'severity', multi: true, values: enumWords.severity }, { param: 'outcome', multi: true, values: enumWords.outcome },
  { param: 'owner', multi: false }, { param: 'preset', multi: false, values: enumWords.preset }, { param: 'maintenance', multi: false, values: enumWords.maintenance },
]

/** The key as shown in a token, in the interface language. */
export function tokenKeyWord(param: string): string {
  return tc('token', param === 'preset' ? 'only' : param)
}

export interface Token { param: string; value: string; key: string; word: string }

function lower(s: string): string {
  return s.toLocaleLowerCase(locale())
}

/** The active filters of a link, one token per value, in the order of tokenKeys. */
export function tokensOf(params: URLSearchParams): Token[] {
  const out: Token[] = []
  for (const k of tokenKeys) {
    const raw = params.get(k.param)
    if (!raw) continue
    const vals = k.multi ? raw.split(',').filter(Boolean) : [raw]
    const words = k.values?.() ?? []
    for (const v of vals) out.push({ param: k.param, value: v, key: tokenKeyWord(k.param), word: words.find((w) => w.value === v)?.word ?? v })
  }
  return out
}

/** Reads "key:value" typed at the end of the search text. null when the key is not one the Explorer filters by, or the
 *  value is not one it takes. rest is the text before it. */
export function parseToken(text: string): { param: string; value: string; rest: string } | null {
  const m = /(^|\s)([^\s:]+):(.+)$/.exec(text)
  if (!m) return null
  const key = lower(m[2])
  const k = tokenKeys.find((x) => x.param === key || lower(tokenKeyWord(x.param)) === key)
  if (!k) return null
  const typed = m[3].trim()
  if (!typed) return null
  let value = typed
  if (k.values) {
    const hit = k.values().find((w) => lower(w.value) === lower(typed) || lower(w.word) === lower(typed))
    if (!hit) return null
    value = hit.value
  }
  return { param: k.param, value, rest: text.slice(0, m.index).trim() }
}

/** Whether the text ends in the start of a known key and a colon: it is being typed as a token, not searched for. */
export function typingToken(text: string): boolean {
  const m = /(^|\s)([^\s:]+):/.exec(text)
  if (!m) return false
  const key = lower(m[2])
  return tokenKeys.some((x) => x.param === key || lower(tokenKeyWord(x.param)) === key)
}

/** The link change that adds a value to a filter (a single-valued filter is replaced). */
export function withToken(params: URLSearchParams, param: string, value: string): Record<string, string | null> {
  const k = tokenKeys.find((x) => x.param === param)
  if (!k?.multi) return { [param]: value }
  const cur = (params.get(param) ?? '').split(',').filter(Boolean)
  return { [param]: cur.includes(value) ? cur.join(',') : [...cur, value].join(',') }
}

/** The link change that removes one value of a filter. */
export function withoutToken(params: URLSearchParams, param: string, value: string): Record<string, string | null> {
  const k = tokenKeys.find((x) => x.param === param)
  if (!k?.multi) return { [param]: null }
  const next = (params.get(param) ?? '').split(',').filter((x) => x && x !== value)
  return { [param]: next.join(',') || null }
}
