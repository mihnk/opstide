import { useEffect, useState } from 'react'
import { getJSON, query, type Entries, type Entry } from '../api'
import { Failed, Loading } from '../components/Shell'
import { useApi } from '../live'
import { Link, navigate, withParams } from '../router'
import { hhmm, hhmmss, isoDate, longDay, offset } from '../time'

const ranges: Record<string, string> = { '24h': '24h', '48h': '48h', '7d': '168h', '30d': '720h' }
const groups = [
  ['runs', 'run outcomes'],
  ['starts', 'run starts'],
  ['schedules', 'schedule changes'],
  ['rollouts', 'rollouts'],
  ['releases', 'releases'],
  ['health', 'workload health'],
  ['pods', 'pod lifecycle'],
  ['kubernetes', 'Kubernetes Events'],
  ['scaling', 'scaling'],
  ['collection', 'collection'],
] as const

const defaultShow = 'runs,schedules,rollouts,releases,health,collection'

const kindWord: Record<string, string> = {
  'job.created': 'Created', 'job.succeeded': 'Succeeded', 'job.failed': 'Failed', 'job.lost': 'Unknown',
  'cronjob.created': 'New CronJob', 'cronjob.deleted': 'Deleted', 'cronjob.suspended': 'Suspended', 'cronjob.resumed': 'Resumed',
  'cronjob.schedule_changed': 'Schedule', 'deployment.rollout': 'Rollout', 'collection.gap': 'Collection',
  'release.observed': 'Release', 'release.superseded': 'Release', 'release.scaled_down': 'Release', 'release.paused': 'Release', 'release.resumed': 'Release',
  'release.stalled': 'Stalled', 'release.failed': 'Failed', 'release.health.degraded': 'Health', 'release.health.healthy': 'Health',
  'release.health.insufficient': 'Health', 'release.unavailable': 'Health',
  'pod.created': 'Pod', 'pod.ready': 'Pod', 'pod.notready': 'NotReady', 'pod.restarted': 'Restart', 'pod.oomkilled': 'OOMKilled',
  'pod.waiting': 'Waiting', 'pod.evicted': 'Evicted', 'pod.terminated': 'Pod',
  'evidence.event': 'K8s Event', 'evidence.count_changed': 'K8s Event',
  'hpa.desired_replicas_changed': 'Scaling', 'hpa.condition_changed': 'Scaling', 'workload.replica_target_changed': 'Scaling',
}

// What a Kubernetes Event entry can be about (the kinds OpsTide keeps Events for).
const evidenceKinds = ['Pod', 'ReplicaSet', 'Deployment', 'Job', 'CronJob', 'HorizontalPodAutoscaler']

export function Log({ params, tz }: { params: URLSearchParams; tz: string }) {
  const range = params.get('range') ?? '48h'
  const q = params.get('q') ?? ''
  const ns = params.get('namespace') ?? ''
  // Run starts are many and say little; they are off unless asked for.
  const reason = params.get('reason') ?? ''
  const kind = params.get('kind') ?? ''
  const workload = params.get('workload') ?? ''
  // Asking for an evidence reason, a resource kind or a workload is asking for Kubernetes Event and scaling entries too.
  const asked = params.get('show')
  const show = asked ?? (reason || kind || workload ? `${defaultShow},kubernetes,scaling` : defaultShow)
  const selected = params.get('entry')
  const severity = params.get('severity') ?? ''
  const outcome = params.get('outcome') ?? ''
  const owner = params.get('owner') ?? ''
  const releaseId = params.get('release') ?? ''
  const path = `/api/v1/events${query({ within: ranges[range] ?? '48h', q, namespace: ns, show, severity, outcome: show.split(',').includes('runs') ? outcome : '', owner, release: releaseId, reason, kind, workload, limit: 100 })}`
  // The owners that exist (deleted CronJobs included), fetched once.
  const [ownerList, setOwnerList] = useState<string[]>([])
  useEffect(() => {
    getJSON<{ owners: string[] }>('/api/v1/owners').then((o) => setOwnerList(o.owners)).catch(() => {})
  }, [])
  const owners = owner && !ownerList.includes(owner) ? [...ownerList, owner] : ownerList
  const runsShown = show.split(',').includes('runs')
  // Run starts are off by default, so a changed Show list narrows the log as well.
  const filtered = !!(q || ns || severity || owner || releaseId || reason || kind || workload || (outcome && runsShown) || show !== defaultShow)
  const { data, error } = useApi<Entries>(path)

  // Older pages, appended below the live first page.
  const [older, setOlder] = useState<{ base: string; entries: Entry[]; cursor?: string }>({ base: path, entries: [] })
  const [loadingMore, setLoadingMore] = useState(false)
  const [text, setText] = useState(q)
  // The link is the truth: clearing filters, or going back, resets the box.
  const [seenQ, setSeenQ] = useState(q)
  if (q !== seenQ) {
    setSeenQ(q)
    setText(q)
  }
  useEffect(() => {
    const t = window.setTimeout(() => {
      if (text !== q) navigate(withParams(params, { q: text || null, entry: null }), true)
    }, 400)
    return () => window.clearTimeout(t)
  }, [text, q, params])

  const pageEntries = older.base === path ? older.entries : []
  const entries = [...(data?.entries ?? []), ...pageEntries]
  const cursor = older.base === path && older.entries.length ? older.cursor : data?.nextCursor
  const sel = entries.find((e) => e.id === selected) ?? null

  const loadMore = async () => {
    if (!cursor) return
    setLoadingMore(true)
    try {
      const p = await getJSON<Entries>(`${path}&cursor=${encodeURIComponent(cursor)}`)
      setOlder((o) => ({ base: path, entries: [...(o.base === path ? o.entries : []), ...p.entries], cursor: p.nextCursor ?? '' }))
    } finally {
      setLoadingMore(false)
    }
  }

  const byDay = new Map<string, Entry[]>()
  for (const e of entries) {
    const d = isoDate(e.at, tz)
    byDay.set(d, [...(byDay.get(d) ?? []), e])
  }
  const shown = new Set(show.split(','))
  const toggle = (g: string) => {
    const next = new Set(shown)
    if (next.has(g)) next.delete(g)
    else next.add(g)
    if (next.size === 0) return
    const value = groups.map(([k]) => k).filter((k) => next.has(k)).join(',')
    navigate(withParams(params, { show: value === defaultShow && !(reason || kind || workload) ? null : value, entry: null }), true)
  }

  return (
    <div className="page" style={{ gap: 18, paddingTop: 24 }}>
      <form className="filters" aria-label="Filter the log" onSubmit={(e) => e.preventDefault()}>
        <label className="field"><span className="label">Find</span>
          <input className="control" style={{ width: 240 }} value={text} placeholder="name, namespace, text" onChange={(e) => setText(e.target.value)} />
        </label>
        <label className="field"><span className="label">Range</span>
          <select className="control" value={range} onChange={(e) => navigate(withParams(params, { range: e.target.value === '48h' ? null : e.target.value }), true)}>
            <option value="24h">Last 24 hours</option>
            <option value="48h">Last 48 hours</option>
            <option value="7d">Last 7 days</option>
            <option value="30d">Last 30 days</option>
          </select>
        </label>
        <label className="field"><span className="label">Namespace</span>
          <input key={ns} className="control" style={{ width: 150 }} defaultValue={ns} placeholder="all"
            onBlur={(e) => e.target.value !== ns && navigate(withParams(params, { namespace: e.target.value.trim() || null }), true)}
            onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} />
        </label>
        <label className="field"><span className="label">Severity</span>
          <select className="control" value={severity} onChange={(e) => navigate(withParams(params, { severity: e.target.value || null, entry: null }), true)}>
            <option value="">Any</option>
            <option value="error">Errors</option>
            <option value="warning,error">Warnings and errors</option>
            {severity && !['error', 'warning,error'].includes(severity) && <option value={severity}>{severity.replace(',', ', ')}</option>}
          </select>
        </label>
        {shown.has('runs') && (
          <label className="field"><span className="label">Run outcome</span>
            <select className="control" value={outcome} onChange={(e) => navigate(withParams(params, { outcome: e.target.value || null, entry: null }), true)}>
              <option value="">Any</option>
              <option value="failed">Failed</option>
              <option value="succeeded">Succeeded</option>
              <option value="unknown">Unknown</option>
              {outcome && !['failed', 'succeeded', 'unknown'].includes(outcome) && <option value={outcome}>{outcome.replace(',', ', ')}</option>}
            </select>
          </label>
        )}
        <label className="field"><span className="label">Resource kind</span>
          <select className="control" value={kind} onChange={(e) => navigate(withParams(params, { kind: e.target.value || null, entry: null }), true)}>
            <option value="">Any</option>
            {evidenceKinds.map((k) => <option key={k} value={k}>{k}</option>)}
            {kind && !evidenceKinds.includes(kind) && <option value={kind}>{kind}</option>}
          </select>
        </label>
        <label className="field"><span className="label">Evidence reason</span>
          <input key={reason} className="control" style={{ width: 150 }} defaultValue={reason} placeholder="e.g. FailedMount" title="The reason Kubernetes reported, exactly as it wrote it"
            onBlur={(e) => e.target.value !== reason && navigate(withParams(params, { reason: e.target.value.trim() || null, entry: null }), true)}
            onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} />
        </label>
        <label className="field"><span className="label">Owner</span>
          <select className="control" value={owner} onChange={(e) => navigate(withParams(params, { owner: e.target.value || null, entry: null }), true)}>
            <option value="">Anyone</option>
            {owners.map((o) => <option key={o} value={o}>{o}</option>)}
          </select>
        </label>
        <fieldset>
          <legend className="label" style={{ padding: '0 0 4px' }}>Show</legend>
          <span className="checks">
            {groups.map(([k, label]) => (
              <label key={k}><input type="checkbox" checked={shown.has(k)} onChange={() => toggle(k)} />{label}</label>
            ))}
          </span>
        </fieldset>
        <span className="grow" style={{ flex: 1 }} />
        <span className="muted" style={{ fontSize: 12 }}>Times {tz} {offset(tz)} · filters are in the link</span>
      </form>

      {workload && (
        <div className="banner" role="note">
          <span className="label">One workload</span>
          <span>Showing entries about one Deployment (by UID <span className="mono">{workload.slice(0, 8)}</span>): Kubernetes Events and scaling entries linked to it. <Link href={withParams(params, { workload: null, entry: null })} replace>Show everything</Link></span>
        </div>
      )}

      {releaseId && (
        <div className="banner" role="note">
          <span className="label">One release</span>
          <span>Showing only the entries of one release. <Link href={`/releases/${encodeURIComponent(releaseId)}`}>Open the release</Link> · <Link href={withParams(params, { release: null, entry: null })} replace>Show everything</Link></span>
        </div>
      )}

      {error && !data && <Failed error={error} />}
      {!data && !error && <Loading />}
      {data && (
        <div className="split">
          <section aria-label="Log entries" className="main">
            {entries.length === 0 && (
              <div className="empty">
                {filtered
                  ? <>Nothing in this range matches these filters. <Link href={withParams(params, { q: null, namespace: null, severity: null, outcome: null, owner: null, release: null, reason: null, kind: null, workload: null, show: null, entry: null })} replace>Clear the filters</Link></>
                  : <>Nothing of the kinds shown was recorded in this range. Run starts are off; if OpsTide was not running, System shows when it was not watching.</>}
              </div>
            )}
            {[...byDay.entries()].map(([d, es]) => (
              <div key={d} className="daygroup">
                <h2>{longDay(es[0].at, tz)}</h2>
                {es.map((e) => (
                  <button key={e.id} type="button" className={`entry ${e.severity}`} aria-pressed={e.id === selected}
                    onClick={() => navigate(withParams(params, { entry: e.id === selected ? null : e.id }), true)}>
                    <span className={`when mono${e.severity === 'error' ? ' fail' : e.confidence === 'unknown' ? ' muted' : ''}`}>{hhmm(e.at, tz)}</span>
                    <span className="label" style={{ color: e.severity === 'error' ? 'var(--fail)' : e.type.startsWith('deployment') ? 'var(--accent)' : undefined }}>
                      {kindWord[e.type] ?? e.type}
                    </span>
                    <span style={{ minWidth: 0 }}>
                      <span>{e.summary}</span>
                      <span className="ref mono">{[e.namespace && e.resourceName ? `${e.namespace}/${e.resourceName}` : e.resourceName, facts(e)].filter(Boolean).join(' · ')}</span>
                    </span>
                  </button>
                ))}
              </div>
            ))}
            {cursor && (
              <button type="button" className="button" style={{ alignSelf: 'flex-start' }} disabled={loadingMore} onClick={loadMore}>
                {loadingMore ? 'Loading…' : 'Older entries'}
              </button>
            )}
          </section>
          <aside aria-label="Entry details">{sel ? <EntryPanel e={sel} tz={tz} /> : <div className="empty">Pick an entry to see where it came from.</div>}</aside>
        </div>
      )}
    </div>
  )
}

function facts(e: Entry): string {
  const f = e.facts ?? {}
  if (e.type === 'job.failed' || e.type === 'job.succeeded') return f.seconds ? `${Math.round(Number(f.seconds) / 60)} min` : ''
  if (e.type === 'collection.gap') return `${f.scope ?? ''}`
  if (e.releaseId && f.revision) return `revision ${f.revision}`
  return ''
}

function EntryPanel({ e, tz }: { e: Entry; tz: string }) {
  const f = e.facts ?? {}
  const utc = (t: string) => <span className="muted"> {hhmmss(t, 'UTC')} UTC</span>
  return (
    <>
      <div className="panel-head">
        <span className="label" style={{ color: e.severity === 'error' ? 'var(--fail)' : undefined }}>{kindWord[e.type] ?? e.type}</span>
        <span className="name">{e.summary}</span>
        {e.resourceName && <span className="mono muted" style={{ fontSize: 12 }}>{e.namespace ? `${e.namespace}/` : ''}{e.resourceName}</span>}
      </div>
      <dl className="facts">
        <dt className="label">Occurred</dt>
        <dd className="mono">{e.occurredAt ? <>{hhmmss(e.occurredAt, tz)}{utc(e.occurredAt)}</> : <span className="muted">the source gives no time</span>}</dd>
        <dt className="label">Observed</dt><dd className="mono">{hhmmss(e.observedAt, tz)}</dd>
        <dt className="label">Recorded</dt><dd className="mono">{hhmmss(e.recordedAt, tz)}</dd>
        <dt className="label">Basis</dt>
        <dd>{e.confidence === 'derived' ? `Derived from ${f.basis ? `the Job's condition ${f.basis}` : 'observed values'}` : e.confidence === 'observed' ? 'Observed from the Kubernetes API' : 'Unknown: OpsTide could not see it happen'}{f.reason ? <>, reason <span className="mono">{f.reason}</span></> : null}</dd>
        {f.scheduledAt && (<><dt className="label">Scheduled</dt><dd className="mono">{hhmm(f.scheduledAt, tz)} <span className="muted">from the {f.scheduledBasis === 'annotation' ? 'scheduled-timestamp annotation' : 'Job name'}</span></dd></>)}
        {f.attempts && (<><dt className="label">Attempts</dt><dd className="mono">{f.attempts}</dd></>)}
        {e.type === 'collection.gap' && f.from && f.to && (<><dt className="label">Not watching</dt><dd className="mono">{hhmmss(f.from, tz)} – {hhmmss(f.to, tz)}</dd></>)}
        {e.type.startsWith('evidence.') && (<>
          <dt className="label">Kubernetes reported</dt><dd><span className="mono">{f.reason}</span> <span className="muted">· {f.eventType}{f.count ? ` · count ${f.count}${f.countBasis ? ` (${f.countBasis})` : ''}` : ''}</span></dd>
          <dt className="label">Message</dt><dd style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{f.message || <span className="muted">none reported</span>}</dd>
          <dt className="label">Source</dt><dd className="mono">{f.source}{f.origin === 'adopted' ? ' · adopted: already present when OpsTide began watching' : ''}</dd>
        </>)}
        {e.type.startsWith('hpa.') && (<>
          <dt className="label">Change</dt><dd className="mono">{f.condition ? `${f.condition}: ${f.from} to ${f.to}${f.reason ? ` (${f.reason})` : ''}` : `desired replicas ${f.from} to ${f.to}`}</dd>
          {f.message && (<><dt className="label">Message</dt><dd style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{f.message}</dd></>)}
          {f.targetRelationship && (<><dt className="label">Target</dt><dd>{f.target} <span className="muted">· {f.targetRelationship}, {f.targetVia}</span></dd></>)}
        </>)}
        {e.type === 'workload.replica_target_changed' && (<><dt className="label">Change</dt><dd className="mono">replica target {f.from} to {f.to}</dd></>)}
        {e.resourceUid && (<><dt className="label">Identity</dt><dd className="mono">{e.kind?.toLowerCase()} {e.resourceUid}</dd></>)}
      </dl>
      <span className="muted" style={{ fontSize: 13 }}>
        OpsTide does not keep container logs.{' '}
        {e.releaseId && <Link href={`/releases/${encodeURIComponent(e.releaseId)}`}>Open the release</Link>}
        {e.workloadId && <>{' '}<Link href={`/log?workload=${encodeURIComponent(e.workloadId)}`}>Entries linked to this workload</Link></>}
        {e.operationId && <Link href={`/operations/${encodeURIComponent(e.operationId)}${e.runId ? `?run=${encodeURIComponent(e.runId)}` : ''}`}>Open the operation</Link>}
      </span>
    </>
  )
}
