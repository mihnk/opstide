import { useEffect, useState } from 'react'
import { getJSON, query, type WorkloadList, type WorkloadView } from '../api'
import { Failed, Loading } from '../components/Shell'
import { rolloutWords } from '../components/status'
import { useApi } from '../live'
import { Link, navigate, withParams } from '../router'
import { relativeTime } from '../time'
import { Note } from '../components/Note'
import { t, tn } from '../i18n'

// What a rollout state means, in Kubernetes' terms. None of these says a workload is healthy or broken.
export const workloadRollout: Record<string, string> = {
  completed: 'Update complete', updating: 'Updating', partitioned: 'At partition boundary',
  awaiting_pod_deletion: 'Awaiting Pod deletion (OnDelete)', unknown: 'Status not yet reported',
}

export function rolloutLabel(kind: string, state: string): string {
  if (kind === 'Deployment') return t((rolloutWords as Record<string, string>)[state] ?? state)
  return t(workloadRollout[state] ?? state)
}

const ranges: [string, string][] = [['24h', '24h'], ['7d', '168h'], ['30d', '720h'], ['any', '']]
const kinds = ['Deployment', 'StatefulSet', 'DaemonSet']

export function Workloads({ params, tz }: { params: URLSearchParams; tz: string }) {
  const range = params.get('range') ?? 'any'
  const ns = params.get('namespace') ?? ''
  const kind = params.get('kind') ?? ''
  const name = params.get('q') ?? ''
  const status = params.get('status') ?? ''
  const path = `/api/v1/workloads${query({ within: ranges.find(([k]) => k === range)?.[1] ?? '', namespace: ns, kind, q: name, status, limit: 100 })}`
  const { data, error } = useApi<WorkloadList>(path)
  const [older, setOlder] = useState<{ base: string; rows: WorkloadView[]; cursor?: string }>({ base: path, rows: [] })
  const [loadingMore, setLoadingMore] = useState(false)
  const [text, setText] = useState(name)
  const [seenQ, setSeenQ] = useState(name)
  if (name !== seenQ) {
    setSeenQ(name)
    setText(name)
  }
  useEffect(() => {
    const t = window.setTimeout(() => {
      if (text !== name) navigate(withParams(params, { q: text || null }), true)
    }, 400)
    return () => window.clearTimeout(t)
  }, [text, name, params])
  const set = (change: Record<string, string | null>) => navigate(withParams(params, change), true)

  const first = data?.workloads ?? []
  const seen = new Set(first.map((w) => w.id))
  const rows = [...first, ...(older.base === path ? older.rows : []).filter((w) => !seen.has(w.id))]
  const cursor = older.base === path && older.rows.length ? older.cursor : data?.nextCursor
  const loadMore = async () => {
    if (!cursor) return
    setLoadingMore(true)
    try {
      const p = await getJSON<WorkloadList>(`${path}&cursor=${encodeURIComponent(cursor)}`)
      setOlder((o) => ({ base: path, rows: [...(o.base === path ? o.rows : []), ...p.workloads], cursor: p.nextCursor ?? '' }))
    } finally {
      setLoadingMore(false)
    }
  }
  const filtered = !!(ns || kind || name || status || range !== 'any')
  const [now] = useState(() => Date.now())

  return (
    <div className="page" style={{ gap: 18 }}>
      <div className="head-row" style={{ gap: '12px 28px' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">{t('Deployments, StatefulSets and DaemonSets observed by OpsTide')}</span>
          <h1 className="title">{data ? (cursor ? t('{n}+ workloads', { n: rows.length }) : tn(rows.length, '{n} workload', '{n} workloads')) : t('Workloads')}</h1>
        </div>
        <span className="grow" />
        <Link href="/releases" style={{ fontSize: 13 }}>{t('Deployment releases')}</Link>
      </div>

      <form className="filters" aria-label={t('Filter workloads')} onSubmit={(e) => e.preventDefault()}>
        <div className="field"><span className="label" id="wl-range">{t('Changed in')}</span>
          <div className="seg" role="group" aria-labelledby="wl-range">
            {ranges.map(([k]) => <button key={k} type="button" aria-pressed={range === k} onClick={() => set({ range: k === 'any' ? null : k })}>{t(k === 'any' ? 'any time' : k)}</button>)}
          </div>
        </div>
        <label className="field"><span className="label">{t('Kind')}</span>
          <select className="control" value={kind} onChange={(e) => set({ kind: e.target.value || null })}>
            <option value="">{t('All kinds')}</option>
            {kinds.map((k) => <option key={k} value={k}>{k}</option>)}
          </select>
        </label>
        <label className="field"><span className="label">{t('Namespace')}</span>
          <input key={ns} className="control" style={{ width: 150 }} defaultValue={ns} placeholder={t('all')}
            onBlur={(e) => e.target.value.trim() !== ns && set({ namespace: e.target.value.trim() || null })}
            onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} />
        </label>
        <label className="field"><span className="label">{t('Name')}</span>
          <input className="control" style={{ width: 180 }} value={text} placeholder={t('part of a name')} onChange={(e) => setText(e.target.value)} />
        </label>
        <label className="field"><span className="label">{t('Observation')}</span>
          <select className="control" value={status} onChange={(e) => set({ status: e.target.value || null })}>
            <option value="">{t('Live and deleted')}</option>
            <option value="live">{t('Still present')}</option>
            <option value="deleted">{t('Deleted (history kept)')}</option>
          </select>
        </label>
        <span className="grow" style={{ flex: 1 }} />
        {filtered && <Link href={location.pathname} replace style={{ fontSize: 13 }}>{t('Clear the filters')}</Link>}
      </form>

      {data && data.notes.length > 0 && <div className="banner" role="note"><span className="label">{t('Coverage gap')}</span><span>{data.notes.map((n, i) => <span key={i} style={{ display: 'block' }}>{n}</span>)}</span></div>}
      {error && !data && <Failed error={error} />}
      {!data && !error && <Loading />}
      {data && rows.length === 0 && <div className="empty">{filtered ? t('No workload matches these filters.') : t('No workload has been recorded yet.')}</div>}
      {rows.length > 0 && (
        <div className="table-wrap">
          <table className="data" style={{ minWidth: 980 }}>
            <thead><tr><th>{t('Workload')}</th><th>{t('Revision')}</th><th>{t('Update state')}</th><th>{t('Desired / ready / available')}</th><th>{t('Last change')}</th><th>{t('Warning Events, 24 h')}</th></tr></thead>
            <tbody>
              {rows.map((w) => <Row key={w.id} w={w} tz={tz} now={now} />)}
            </tbody>
          </table>
        </div>
      )}
      {cursor && <button type="button" className="button" style={{ alignSelf: 'flex-start' }} disabled={loadingMore} onClick={loadMore}>{loadingMore ? t('Loading…') : t('Older changes')}</button>}
      <Note block more={t('Ordered by the last change Kubernetes reported to OpsTide. There is no score: counts are Kubernetes\' own, the update state follows a documented rule per kind, and an absent Warning Event means none was recorded, not that nothing is wrong.')}>{t('Ordered by last reported change · no score')}</Note>
    </div>
  )
}

function Row({ w, tz, now }: { w: WorkloadView; tz: string; now: number }) {
  const st = w.state
  const rev = st ? (st.updateRevision && st.updateRevision !== st.revision && w.kind !== 'Deployment' ? `${st.revision || '—'} → ${st.updateRevision}` : st.revision || '—') : '—'
  return (
    <tr>
      <td>
        <span className="label">{w.kind}{w.origin === 'adopted' ? ` · ${t('adopted')}` : ''}</span>
        <Link href={`/workloads/${encodeURIComponent(w.id)}`} className="mono" style={{ display: 'block', overflowWrap: 'anywhere' }}>{w.namespace}/{w.name}</Link>
        {w.status === 'deleted' && <span className="chip">{t('deleted')}</span>}
      </td>
      <td className="mono" style={{ overflowWrap: 'anywhere' }}>{rev}</td>
      <td>{st ? <span title={st.rolloutNote}>{rolloutLabel(w.kind, st.rollout)}</span> : <span className="muted">{t('not recorded yet')}</span>}</td>
      <td className="mono">{st ? `${st.desired} / ${st.ready} / ${st.available}` : '—'}</td>
      <td>{st?.lastChangeAt ? <><span title={`${st.lastChangeAt} UTC`}>{relativeTime(st.lastChangeAt, new Date(now).toISOString(), tz)}</span>{st.changeBasis === 'baseline' && <span className="chip" title={t('Not a change OpsTide saw: the newest time the records or the source give for something already there')}>{t('baseline')}</span>}</> : <span className="muted">—</span>}</td>
      <td>{w.evidence.warnings24h > 0
        ? <><span className="mono">{w.evidence.warnings24h}</span> <span className="muted">{t('· latest {reason}', { reason: w.evidence.latestWarningReason ?? '' })}</span></>
        : <span className="muted">{t('none recorded')}</span>}</td>
    </tr>
  )
}
