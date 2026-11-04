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
  ['collection', 'collection'],
] as const

const defaultShow = 'runs,schedules,rollouts,collection'

const kindWord: Record<string, string> = {
  'job.created': 'Created', 'job.succeeded': 'Succeeded', 'job.failed': 'Failed', 'job.lost': 'Unknown',
  'cronjob.created': 'New CronJob', 'cronjob.deleted': 'Deleted', 'cronjob.suspended': 'Suspended', 'cronjob.resumed': 'Resumed',
  'cronjob.schedule_changed': 'Schedule', 'deployment.rollout': 'Rollout', 'collection.gap': 'Collection',
}

export function Log({ params, tz }: { params: URLSearchParams; tz: string }) {
  const range = params.get('range') ?? '48h'
  const q = params.get('q') ?? ''
  const ns = params.get('namespace') ?? ''
  // Run starts are many and say little; they are off unless asked for.
  const show = params.get('show') ?? defaultShow
  const selected = params.get('entry')
  const path = `/api/v1/events${query({ within: ranges[range] ?? '48h', q, namespace: ns, show, limit: 100 })}`
  const { data, error } = useApi<Entries>(path)

  // Older pages, appended below the live first page.
  const [older, setOlder] = useState<{ base: string; entries: Entry[]; cursor?: string }>({ base: path, entries: [] })
  const [loadingMore, setLoadingMore] = useState(false)
  const [text, setText] = useState(q)
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
    navigate(withParams(params, { show: value === defaultShow ? null : value, entry: null }), true)
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
          <input className="control" style={{ width: 150 }} defaultValue={ns} placeholder="all"
            onBlur={(e) => e.target.value !== ns && navigate(withParams(params, { namespace: e.target.value.trim() || null }), true)}
            onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} />
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

      {error && !data && <Failed error={error} />}
      {!data && !error && <Loading />}
      {data && (
        <div className="split">
          <section aria-label="Log entries" className="main">
            {entries.length === 0 && <div className="empty">No entries match. Widen the range or clear the filters.</div>}
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
        {f.from && f.to && (<><dt className="label">Not watching</dt><dd className="mono">{hhmmss(f.from, tz)} – {hhmmss(f.to, tz)}</dd></>)}
        {e.resourceUid && (<><dt className="label">Identity</dt><dd className="mono">{e.kind?.toLowerCase()} {e.resourceUid}</dd></>)}
      </dl>
      <span className="muted" style={{ fontSize: 13 }}>
        OpsTide does not keep container logs.{' '}
        {e.operationId && <Link href={`/operations/${encodeURIComponent(e.operationId)}${e.runId ? `?run=${encodeURIComponent(e.runId)}` : ''}`}>Open the operation</Link>}
      </span>
    </>
  )
}
