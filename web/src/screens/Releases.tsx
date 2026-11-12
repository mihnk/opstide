import { useEffect, useState } from 'react'
import { getJSON, query, type ReleaseRow, type Releases as ReleasesData } from '../api'
import { HealthStatus, RolloutStatus, healthWords, rolloutWords } from '../components/status'
import { Failed, Loading } from '../components/Shell'
import { useApi } from '../live'
import { Link, navigate, withParams } from '../router'
import { ago, hhmm, offset, relativeTime } from '../time'
import { t, tn, ts } from '../i18n'

const ranges: [string, string][] = [['24h', '24h'], ['48h', '48h'], ['7d', '168h'], ['30d', '720h']]

export function Releases({ params, tz }: { params: URLSearchParams; tz: string }) {
  const range = params.get('range') ?? '7d'
  const ns = params.get('namespace') ?? ''
  const workload = params.get('workload') ?? ''
  const state = params.get('state') ?? ''
  const health = params.get('health') ?? ''
  const path = `/api/v1/releases${query({ within: ranges.find(([k]) => k === range)?.[1] ?? '168h', namespace: ns, workload, state, health, limit: 50 })}`
  const { data, error } = useApi<ReleasesData>(path)

  // Older pages, appended below the live first page.
  const [older, setOlder] = useState<{ base: string; rows: ReleaseRow[]; cursor?: string }>({ base: path, rows: [] })
  const [loadingMore, setLoadingMore] = useState(false)
  const [text, setText] = useState(workload)
  const [seenW, setSeenW] = useState(workload)
  if (workload !== seenW) {
    setSeenW(workload)
    setText(workload)
  }
  useEffect(() => {
    const t = window.setTimeout(() => {
      if (text !== workload) navigate(withParams(params, { workload: text || null }), true)
    }, 400)
    return () => window.clearTimeout(t)
  }, [text, workload, params])

  const first = data?.releases ?? []
  const seen = new Set(first.map((r) => r.id))
  const rows = [...first, ...(older.base === path ? older.rows : []).filter((r) => !seen.has(r.id))]
  const cursor = older.base === path && older.rows.length ? older.cursor : data?.nextCursor
  const loadMore = async () => {
    if (!cursor) return
    setLoadingMore(true)
    try {
      const p = await getJSON<ReleasesData>(`${path}&cursor=${encodeURIComponent(cursor)}`)
      setOlder((o) => ({ base: path, rows: [...(o.base === path ? o.rows : []), ...p.releases], cursor: p.nextCursor ?? '' }))
    } finally {
      setLoadingMore(false)
    }
  }
  const filtered = !!(ns || workload || state || health)
  const set = (change: Record<string, string | null>) => navigate(withParams(params, change), true)

  return (
    <div className="page" style={{ gap: 18 }}>
      <div className="head-row" style={{ gap: '12px 28px' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">{t('Deployments observed by OpsTide')}</span>
          <h1 className="title">{data ? (cursor ? t('{n}+ releases', { n: rows.length }) : tn(rows.length, '{n} release', '{n} releases')) : t('Releases')}</h1>
        </div>
      </div>

      <form className="filters" aria-label={t('Filter releases')} onSubmit={(e) => e.preventDefault()}>
        <div className="field"><span className="label" id="range-l">{t('Range')}</span>
          <div className="seg" role="group" aria-labelledby="range-l">
            {ranges.map(([k]) => <button key={k} type="button" aria-pressed={range === k} onClick={() => set({ range: k === '7d' ? null : k })}>{t(k)}</button>)}
          </div>
        </div>
        <label className="field"><span className="label">{t('Namespace')}</span>
          <input key={ns} className="control" style={{ width: 150 }} defaultValue={ns} placeholder={t('all')}
            onBlur={(e) => e.target.value.trim() !== ns && set({ namespace: e.target.value.trim() || null })}
            onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} />
        </label>
        <label className="field"><span className="label">{t('Workload')}</span>
          <input className="control" style={{ width: 170 }} value={text} placeholder={t('name contains')} onChange={(e) => setText(e.target.value)} />
        </label>
        <label className="field"><span className="label">{t('Rollout status')}</span>
          <select className="control" value={state} onChange={(e) => set({ state: e.target.value || null })}>
            <option value="">{t('Any')}</option>
            {Object.entries(rolloutWords).map(([k, v]) => <option key={k} value={k}>{t(v)}</option>)}
          </select>
        </label>
        <label className="field"><span className="label">{t('Rule result')}</span>
          <select className="control" value={health} onChange={(e) => set({ health: e.target.value || null })}>
            <option value="">{t('Any')}</option>
            {Object.entries(healthWords).map(([k, v]) => <option key={k} value={k}>{t(v)}</option>)}
          </select>
        </label>
        <span className="grow" style={{ flex: 1 }} />
        <span className="muted fine">{t('Times {tz} {offset} · filters are in the link · observation window {n} min', { tz, offset: offset(tz), n: data?.windowMinutes ?? '…' })}</span>
      </form>

      {!!data?.warnings.length && (
        <div className="banner" role="note">
          <span className="label">{t('Coverage gap')}</span>
          <span>{data.warnings.map((w, i) => <span key={i} style={{ display: 'block' }}>{ts(w)}{i === 0 && <> <Link href="/system">{t('See System')}</Link></>}</span>)}</span>
        </div>
      )}

      {error && !data && <Failed error={error} />}
      {!data && !error && <Loading />}
      {data && rows.length === 0 && (
        <div className="empty">
          {filtered
            ? <>{t('No release in this range matches these filters.')} <Link href={withParams(params, { namespace: null, workload: null, state: null, health: null })} replace>{t('Clear the filters')}</Link></>
            : <>{t('OpsTide has not seen a Deployment revision in this range. A release is recorded when a new revision appears while OpsTide is watching; revisions that were already there are shown as adopted. Check System for the Deployment, ReplicaSet and Pod watches.')}</>}
        </div>
      )}
      {rows.length > 0 && (
        <div className="table-wrap">
          <table className="data compact" style={{ minWidth: 940 }}>
            <thead>
              <tr>{['Workload', 'Revision', 'First observed', 'Rollout', 'Rule result', 'Available / desired', 'Recent events'].map((h) => <th key={h} scope="col" className="label">{t(h)}</th>)}</tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const href = `/releases/${encodeURIComponent(r.id)}`
                return (
                  <tr key={r.id} className="clickable" onClick={(e) => { if (!(e.target as HTMLElement).closest('a')) navigate(href) }}>
                    <td>
                      <Link href={href} style={{ display: 'flex', flexDirection: 'column', color: 'var(--ink)', textDecoration: 'none' }}>
                        <span style={{ fontWeight: 500 }}>{r.workload}</span>
                        <span className="mono muted fine">{r.namespace}/{r.workload}</span>
                      </Link>
                    </td>
                    <td className="mono">{r.revision}{r.origin === 'adopted' && <span className="chip" title={t('It existed before OpsTide started watching: its rollout was not observed.')}>{t('adopted')}</span>}</td>
                    <td className="mono fine">
                      {relativeTime(r.observedAt, data?.now ?? r.observedAt, tz)}
                      <span className="muted sub" title={`${r.observedAt} UTC`}>{ago(r.observedAt, Date.parse(data?.now ?? r.observedAt))}</span>
                    </td>
                    <td><RolloutStatus state={r.state} />{r.paused && <span className="muted sub">{t('paused')}</span>}</td>
                    <td>
                      <HealthStatus health={r.health} />
                      <span className="muted sub">
                        {r.health === 'still_observing' && r.observeUntil ? t('observing until {time}', { time: hhmm(r.observeUntil, tz) }) : ts(r.healthReasons[0])}
                      </span>
                    </td>
                    <td className="mono">{r.available} / {r.desired}</td>
                    <td className="fine">
                      {r.recent.length === 0 ? <span className="muted">{t('none in window')}</span> : r.recent.map((b, i) => (
                        <span key={i} style={{ display: 'block', color: b.severity === 'error' ? 'var(--fail)' : undefined }}>{b.summary}</span>
                      ))}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {cursor && (
        <button type="button" className="button" style={{ alignSelf: 'flex-start' }} disabled={loadingMore} onClick={loadMore}>{loadingMore ? t('Loading…') : t('Older releases')}</button>
      )}
      <div className="legend" style={{ paddingTop: 0 }}>
        <span>{t("Rollout is what Kubernetes reports. Rule result is what OpsTide's rules found in the minutes after it.")}</span>
        <span>{t('A release OpsTide did not see start is marked adopted and is never assessed.')}</span>
      </div>
    </div>
  )
}
