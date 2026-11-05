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
  type: string
  namespace?: string
  kind?: string
  resourceName?: string
  resourceUid?: string
  operationId?: string
  runId?: string
  at: string
  occurredAt?: string
  observedAt: string
  recordedAt: string
  severity: 'info' | 'warning' | 'error'
  confidence: 'observed' | 'derived' | 'unknown'
  summary: string
  facts?: Record<string, string>
}

export interface Entries { schema: number; entries: Entry[]; nextCursor?: string }

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
  }
  integrations: { name: string; state: string; note: string }[]
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
