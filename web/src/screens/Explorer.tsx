import { useEffect, useMemo, useState } from 'react'
import { getJSON, query } from '../api'
import { Coverage } from '../components/Coverage'
import { Failed, Loading } from '../components/Shell'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import {
  emptyTitle, groupWords, provWord, quickRanges, type ExplorerContext, type ExplorerFilters, type ExplorerList, type ExplorerRecord,
} from '../explorer'
import { useApi } from '../live'
import { t, tn } from '../i18n'
import { Link, navigate, withParams } from '../router'
import { hhmm, hhmmss, isoDate, longDay, offset, utcToZoned, zonedToUtc } from '../time'
import { addToWorkspace, useWorkspace, WORKSPACE_MAX } from '../workspace'
import { RecordPanel } from './ExplorerPanel'
import { ZoneSelect } from './common'
import { Note } from '../components/Note'

// Explorer: everything OpsTide recorded, on one timeline. It shows evidence; it does not interpret it. Routine records
// are hidden by default and counted; nothing else is hidden. Filters live in the link (no token, no Event message).

const scopeKeys = ['namespace', 'uid', 'name', 'workload', 'release', 'run', 'operation', 'node', 'kind'] as const
const identityLabel: Record<string, string> = { uid: 'resource UID', name: 'name', workload: 'workload', release: 'release', run: 'run', operation: 'operation', node: 'Node', kind: 'kind', namespace: 'namespace' }

function list(v: string | null): string[] {
  return v ? v.split(',').filter(Boolean) : []
}

/** Identity used to group: the UID when the record has one, else kind, namespace and name. Never a guess. */
function identityOf(r: ExplorerRecord): { key: string; label: string } {
  const x = r.resource
  if (!x.kind && !x.name && !x.uid) return { key: `~${r.id}`, label: t('no resource named') }
  const key = x.uid ? `${x.kind}|${x.uid}` : `${x.kind}|${x.namespace ?? ''}|${x.name ?? ''}`
  return { key, label: `${x.kind || t('Resource')} ${x.namespace ? `${x.namespace}/` : ''}${x.name ?? x.uid ?? ''}` }
}

function Row({ r, tz, selected, onPick }: { r: ExplorerRecord; tz: string; selected: boolean; onPick: () => void }) {
  return (
    <div className={`erow ${r.severity ?? ''}${selected ? ' sel' : ''}`}>
      <span className="when mono">{hhmm(r.at, tz)}</span>
      <span className="label etype" title={r.type}>{r.group === 'events' ? (r.eventType ?? 'Event') : t(r.group)}</span>
      <button type="button" className="ebody" aria-pressed={selected} onClick={onPick}>
        <span>{r.summary}{r.maintenance && <> <span className="chip maint" title={`${r.maintenance.statement} · ${provWord(r.maintenance.provenance)}`}>{t('maintenance: {name}', { name: r.maintenance.name })}</span></>}</span>
        <span className="ref mono">
          {[r.severity === 'error' || r.severity === 'warning' ? t(r.severity) : '', r.resource.name ? `${r.resource.kind} ${r.resource.namespace ? `${r.resource.namespace}/` : ''}${r.resource.name}` : '', r.count && r.count.value > 1 ? `×${r.count.value}` : '', r.type !== 'kubernetes.event' ? r.type : ''].filter(Boolean).join(' · ')}
        </span>
      </button>
      <span className={`chip prov-${r.provenance.toLowerCase()}`} title={r.source}>{provWord(r.provenance)}</span>
      <WorkspaceToggle id={r.id} compact />
    </div>
  )
}

export function Explorer({ params, tz, setTz }: { params: URLSearchParams; tz: string; setTz: (z: string) => void }) {
  const get = (k: string) => params.get(k) ?? ''
  const custom = !!(params.get('from') || params.get('to'))
  const range = custom ? '' : get('range') || '24h'
  const filter = {
    range, from: get('from'), to: get('to'), q: get('q'), namespace: get('namespace'), kind: get('kind'), name: get('name'), uid: get('uid'), workload: get('workload'),
    release: get('release'), run: get('run'), operation: get('operation'), node: get('node'), group: get('group'), type: get('type'), reason: get('reason'),
    source: get('source'), provenance: get('provenance'), severity: get('severity'), outcome: get('outcome'), owner: get('owner'), preset: get('preset'), routine: get('routine'),
    maintenance: get('maintenance'),
  }
  const rec = get('rec')
  const groupBy = get('by') === 'resource'
  const path = `/api/v1/explorer${query({ ...filter, limit: 100 })}`
  const { data, error } = useApi<ExplorerList>(path, { keep: true })
  const scopeOnly = { range: filter.range, from: filter.from, to: filter.to, namespace: filter.namespace, uid: filter.uid, name: filter.name, workload: filter.workload, release: filter.release, run: filter.run, operation: filter.operation, node: filter.node }
  const { data: facets } = useApi<ExplorerFilters>(`/api/v1/explorer/filters${query(scopeOnly)}`, { keep: true })
  const { data: ctx } = useApi<ExplorerContext>(data ? `/api/v1/explorer/context${query({ from: data.from, to: data.to, namespace: filter.namespace })}` : null, { keep: true })

  const set = (change: Record<string, string | null>) => navigate(withParams(params, { rec: null, ...change }), true)

  // Older pages, appended below the live first page.
  // The first page is frozen when older ones are loaded: a live refresh of it would otherwise slide records between the pages.
  const [older, setOlder] = useState<{ base: string; records: ExplorerRecord[]; cursor?: string; head?: ExplorerRecord[] }>({ base: path, records: [] })
  const [more, setMore] = useState(false)
  const cursor = older.base === path && older.records.length ? older.cursor : data?.nextCursor
  const loadMore = async () => {
    if (!cursor || !data) return
    setMore(true)
    try {
      // The first page's own bounds, so the range does not slide while paging.
      const p = await getJSON<ExplorerList>(`/api/v1/explorer${query({ ...filter, range: '', from: data.from, to: data.to, limit: 100, cursor })}`)
      setOlder((o) => ({ base: path, head: o.base === path && o.head ? o.head : data.records, records: [...(o.base === path ? o.records : []), ...p.records], cursor: p.nextCursor ?? '' }))
    } finally {
      setMore(false)
    }
  }

  const [text, setText] = useState(filter.q)
  const [seenQ, setSeenQ] = useState(filter.q)
  if (filter.q !== seenQ) {
    setSeenQ(filter.q)
    setText(filter.q)
  }
  useEffect(() => {
    const timer = window.setTimeout(() => {
      if (text !== filter.q) navigate(withParams(params, { q: text || null, rec: null }), true)
    }, 400)
    return () => window.clearTimeout(timer)
  }, [text, filter.q, params])

  useEffect(() => {
    if (!rec) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') navigate(withParams(new URLSearchParams(location.search), { rec: null }), true)
    }
    window.addEventListener('keydown', onKey)
    document.getElementById('record-title')?.focus()
    return () => window.removeEventListener('keydown', onKey)
  }, [rec])

  const frozen = older.base === path ? older.head : undefined
  const newerArrived = !!frozen && !!data && data.records.some((r) => !frozen.some((f) => f.id === r.id) && r.at >= (frozen[0]?.at ?? ''))
  const liveRecords = data?.records
  const records = useMemo(() => {
    const first = (older.base === path ? older.head : undefined) ?? liveRecords ?? []
    const seen = new Set(first.map((r) => r.id))
    return [...first, ...(older.base === path ? older.records : []).filter((r) => !seen.has(r.id))]
  }, [older, path, liveRecords])
  const byDay = new Map<string, ExplorerRecord[]>()
  for (const r of records) byDay.set(isoDate(r.at, tz), [...(byDay.get(isoDate(r.at, tz)) ?? []), r])

  const groups = list(filter.group)
  const provs = list(filter.provenance)
  const toggleIn = (key: 'group' | 'provenance', cur: string[], v: string) => {
    const next = cur.includes(v) ? cur.filter((x) => x !== v) : [...cur, v]
    set({ [key]: next.join(',') || null })
  }
  const scopeChips = scopeKeys.filter((k) => filter[k])
  const filtered = !!(filter.q || filter.group || filter.type || filter.reason || filter.source || filter.provenance || filter.severity || filter.outcome || filter.owner || filter.preset || filter.maintenance || scopeChips.length)
  // The owners that exist (deleted CronJobs included), fetched once.
  const [ownerList, setOwnerList] = useState<string[]>([])
  useEffect(() => {
    getJSON<{ owners: string[] }>('/api/v1/owners').then((o) => setOwnerList(o.owners)).catch(() => {})
  }, [])
  const owners = filter.owner && !ownerList.includes(filter.owner) ? [...ownerList, filter.owner] : ownerList
  const clearAll = withParams(params, { q: null, namespace: null, kind: null, name: null, uid: null, workload: null, release: null, run: null, operation: null, node: null, group: null, type: null, reason: null, source: null, provenance: null, severity: null, outcome: null, owner: null, preset: null, routine: null, maintenance: null, rec: null })
  const wsIds = useWorkspace()

  // Custom range inputs: wall-clock in the chosen zone.
  // Until edited, the boxes show the range now on screen.
  const [editFrom, setCFrom] = useState<string | null>(null)
  const [editTo, setCTo] = useState<string | null>(null)
  const cFrom = editFrom ?? (data ? utcToZoned(Date.parse(data.from), tz) : '')
  const cTo = editTo ?? (data ? utcToZoned(Date.parse(data.to), tz) : '')
  const [showCustom, setShowCustom] = useState(custom)
  const applyCustom = () => {
    const a = new Date(zonedToUtc(cFrom, tz))
    const b = new Date(zonedToUtc(cTo, tz))
    if (isNaN(a.getTime()) || isNaN(b.getTime()) || b <= a) return
    setCFrom(null)
    setCTo(null)
    set({ range: null, from: a.toISOString().replace('.000Z', 'Z'), to: b.toISOString().replace('.000Z', 'Z') })
  }

  const [msgs, setMsgs] = useState(false)
  const [added, setAdded] = useState('')
  const rows = (rs: ExplorerRecord[]) => rs.map((r) => <Row key={r.id} r={r} tz={tz} selected={r.id === rec} onPick={() => navigate(withParams(params, { rec: r.id === rec ? null : r.id }), true)} />)

  return (
    <div className="page" style={{ gap: 16, paddingTop: 24 }}>
      <div className="head-row" style={{ gap: '8px 20px' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">{t('Explorer')}</span>
          <h1 className="title" style={{ fontSize: 28 }}>{t('What OpsTide recorded')}</h1>
        </div>
        <span className="grow" />
        <ZoneSelect tz={tz} setTz={setTz} />
        <Link href="/workspace" className="button" style={{ display: 'inline-flex', alignItems: 'center' }}>{t('Workspace')}{wsIds.length ? ` · ${wsIds.length}` : ''}</Link>
        <details className="export">
          <summary className="button" style={{ display: 'inline-flex', alignItems: 'center', cursor: 'pointer' }}>{t('Export JSON')}</summary>
          <div className="export-box">
            <p style={{ margin: 0 }}>{t('Downloads what the filters match (oldest first, up to 5,000 records) as a versioned JSON file. The file says if it stopped early. Nothing is sent anywhere.')}</p>
            <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
              <input type="checkbox" checked={msgs} onChange={(e) => setMsgs(e.target.checked)} />
              <span>{t('Include Kubernetes Event messages and quoted condition messages')}{msgs && <b className="fail">{t(' — they can contain names, addresses or other sensitive text, and go into the file.')}</b>}</span>
            </label>
            <a className="button" style={{ alignSelf: 'flex-start', display: 'inline-flex', alignItems: 'center' }} download
              href={`/api/v1/explorer/export${query({ ...filter, messages: msgs ? '1' : '' })}`}>{t('Download filtered view')}</a>
          </div>
        </details>
      </div>

      <form className="filters" aria-label={t('Filter the timeline')} onSubmit={(e) => e.preventDefault()} style={{ gap: '10px 16px' }}>
        <div className="field">
          <span className="label">{t('Range')}</span>
          <span className="seg small" role="group" aria-label={t('Range')}>
            {quickRanges.map((r) => <button key={r} type="button" aria-pressed={range === r} onClick={() => { setShowCustom(false); set({ range: r === '24h' ? null : r, from: null, to: null }) }}>{r}</button>)}
            <button type="button" aria-pressed={custom || showCustom} onClick={() => setShowCustom(true)}>{t('Custom')}</button>
          </span>
        </div>
        {showCustom && (
          <>
            <label className="field"><span className="label">{t('From ({tz})', { tz })}</span><input className="control" type="datetime-local" value={cFrom} onChange={(e) => setCFrom(e.target.value)} /></label>
            <label className="field"><span className="label">{t('To ({tz})', { tz })}</span><input className="control" type="datetime-local" value={cTo} onChange={(e) => setCTo(e.target.value)} /></label>
            <button type="button" className="button" onClick={applyCustom}>{t('Apply')}</button>
          </>
        )}
        <label className="field"><span className="label">{t('Find')}</span>
          <input className="control" style={{ width: 220 }} value={text} placeholder={t('names, reasons, summaries')} onChange={(e) => setText(e.target.value)} />
        </label>
        <label className="field"><span className="label">{t('Namespace')}</span>
          <select className="control" value={filter.namespace} onChange={(e) => set({ namespace: e.target.value || null })}>
            <option value="">{t('All')}</option>
            {filter.namespace && !facets?.namespaces.some((n) => n.value === filter.namespace) && <option value={filter.namespace}>{filter.namespace}</option>}
            {facets?.namespaces.map((n) => <option key={n.value} value={n.value}>{n.value} ({n.count})</option>)}
          </select>
        </label>
        <label className="field"><span className="label">{t('Resource kind')}</span>
          <select className="control" value={filter.kind} onChange={(e) => set({ kind: e.target.value || null })}>
            <option value="">{t('Any')}</option>
            {filter.kind && !facets?.kinds.some((k) => k.value === filter.kind) && <option value={filter.kind}>{filter.kind}</option>}
            {facets?.kinds.map((k) => <option key={k.value} value={k.value}>{k.value} ({k.count})</option>)}
          </select>
        </label>
        <label className="field"><span className="label">{t('Reason')}</span>
          <input key={filter.reason} className="control" style={{ width: 150 }} list="explorer-reasons" defaultValue={filter.reason} placeholder={t('exactly as written')}
            onBlur={(e) => e.target.value !== filter.reason && set({ reason: e.target.value.trim() || null })} onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} />
          <datalist id="explorer-reasons">{facets?.reasons.map((r) => <option key={r.value} value={r.value}>{r.count}</option>)}</datalist>
        </label>
        <label className="field"><span className="label">{t('Source')}</span>
          <select className="control" value={filter.source} onChange={(e) => set({ source: e.target.value || null })}>
            <option value="">{t('Any')}</option>
            {facets?.sources.map((s) => <option key={s} value={s}>{s}</option>)}
          </select>
        </label>
        <label className="field"><span className="label">{t('Severity')}</span>
          <select className="control" value={filter.severity} onChange={(e) => set({ severity: e.target.value || null })}>
            <option value="">{t('Any')}</option>
            <option value="error">{t('Errors')}</option>
            <option value="warning,error">{t('Warnings and errors')}</option>
            {filter.severity && !['error', 'warning,error'].includes(filter.severity) && <option value={filter.severity}>{filter.severity.replace(',', ', ')}</option>}
          </select>
        </label>
        <label className="field"><span className="label">{t('Run outcome')}</span>
          <select className="control" value={filter.outcome} onChange={(e) => set({ outcome: e.target.value || null })}>
            <option value="">{t('Any')}</option>
            <option value="failed">{t('Failed')}</option>
            <option value="succeeded">{t('Succeeded')}</option>
            <option value="unknown">{t('Unknown')}</option>
            {filter.outcome && !['failed', 'succeeded', 'unknown'].includes(filter.outcome) && <option value={filter.outcome}>{filter.outcome.replace(',', ', ')}</option>}
          </select>
        </label>
        <label className="field"><span className="label">{t('Owner')}</span>
          <select className="control" value={filter.owner} onChange={(e) => set({ owner: e.target.value || null })}>
            <option value="">{t('Anyone')}</option>
            {owners.map((o) => <option key={o} value={o}>{o}</option>)}
          </select>
        </label>
        <fieldset>
          <legend className="label" style={{ padding: '0 0 4px' }}>{t('Provenance')}</legend>
          <span className="checks">
            {(['observed', 'derived', 'unknown'] as const).map((p) => (
              <label key={p}><input type="checkbox" checked={provs.includes(p)} onChange={() => toggleIn('provenance', provs, p)} />{provWord(p)}{facets ? ` (${facets.provenance.find((x) => x.value === p)?.count ?? 0})` : ''}</label>
            ))}
          </span>
        </fieldset>
        <fieldset style={{ flexBasis: '100%' }}>
          <legend className="label" style={{ padding: '0 0 4px' }}>{t('Kind of record')} {groups.length === 0 && <span className="muted" style={{ textTransform: 'none', letterSpacing: 0 }}>{t('· all shown')}</span>}</legend>
          <span className="checks" style={{ flexWrap: 'wrap', height: 'auto', rowGap: 6 }}>
            {(facets?.groups ?? Object.keys(groupWords).map((g) => ({ value: g, count: 0 }))).filter((g) => g.value !== 'other').map((g) => (
              <label key={g.value}><input type="checkbox" checked={groups.includes(g.value)} onChange={() => toggleIn('group', groups, g.value)} />{groupWords[g.value] ?? g.value}{facets ? ` (${g.count})` : ''}</label>
            ))}
          </span>
        </fieldset>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input type="checkbox" checked={filter.routine === '1'} onChange={(e) => set({ routine: e.target.checked ? '1' : null })} />
          {t('Show routine records (Pod created/ready/terminated, Normal Events, Job creation, reported counts)')}
          {data && filter.routine !== '1' && data.suppressedRoutine > 0 && <span className="chip" title={t('Left out of the timeline, not deleted')}>{t('{n} hidden', { n: data.suppressedRoutine })}</span>}
        </label>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center' }} title={t('Only records that say something was changed: a spec written (and by which client), a revision, a replica target, a schedule or suspension, a cordon')}>
          <input type="checkbox" checked={filter.preset === 'changes'} onChange={(e) => set({ preset: e.target.checked ? 'changes' : null })} />
          {t('Changes only')}
        </label>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center' }} title={t('Leaves out the records inside a maintenance window declared in the configuration. They are still recorded, and shown when this is off.')}>
          <input type="checkbox" checked={filter.maintenance === 'outside'} onChange={(e) => set({ maintenance: e.target.checked ? 'outside' : null })} />
          {t('Outside maintenance only')}
        </label>
        <label style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          <input type="checkbox" checked={groupBy} onChange={(e) => set({ by: e.target.checked ? 'resource' : null, rec })} />
          {t('Group by resource')}
        </label>
        <span className="grow" style={{ flex: 1 }} />
        <span className="muted" style={{ fontSize: 12 }}>{t('Times {tz} {offset} · filters are in the link', { tz, offset: offset(tz) })}</span>
      </form>

      {scopeChips.length > 0 && (
        <div className="banner" role="note" style={{ flexWrap: 'wrap' }}>
          <span className="label">{t('Scoped to')}</span>
          {scopeChips.map((k) => (
            <span key={k} className="chip" style={{ margin: 0 }}>{t(identityLabel[k])}: <span className="mono">{filter[k].length > 24 ? `${filter[k].slice(0, 12)}…` : filter[k]}</span>{' '}
              <Link href={withParams(params, { [k]: null, rec: null })} replace aria-label={t('Remove the {name} filter', { name: t(identityLabel[k]) })}>✕</Link></span>
          ))}
          <Link href={clearAll} replace>{t('Clear all filters')}</Link>
        </div>
      )}

      {ctx && <Coverage ctx={ctx} tz={tz} />}
      {data && data.notices.length > 0 && (
        <div className="banner" role="note"><span className="label">{t('Not whole')}</span><span>{data.notices.map((n, i) => <span key={i} style={{ display: 'block' }}>{n}</span>)}</span></div>
      )}

      {error && !data && <Failed error={error} />}
      {!data && !error && <Loading />}
      {data && (
        <div className="split">
          <section aria-label={t('Timeline')} className="main">
            <div className="muted" style={{ margin: '0 0 8px', fontSize: 12 }}>
              {hhmmss(data.from, tz)} {isoDate(data.from, tz)} → {hhmmss(data.to, tz)} {isoDate(data.to, tz)} · {t('newest first')} · <Note more={data.orderBasis}>{t('record time order')}</Note>
            </div>
            {records.length === 0 && data.empty && (
              <div className={`empty-box ${data.empty.kind}`} role="status">
                <span className="label">{emptyTitle[data.empty.kind]}</span>
                <b>{data.empty.message}</b>
                {data.empty.detail.map((d, i) => <span key={i} className="muted">{d}</span>)}
                <span style={{ display: 'flex', gap: 14, flexWrap: 'wrap' }}>
                  {data.empty.kind === 'no_matches' && filtered && <Link href={clearAll} replace>{t('Clear the filters')}</Link>}
                  {data.suppressedRoutine > 0 && filter.routine !== '1' && <Link href={withParams(params, { routine: '1' })} replace>{tn(data.suppressedRoutine, 'Show {n} routine record', 'Show {n} routine records')}</Link>}
                  {data.empty.kind !== 'no_matches' && <Link href="/system">{t('See what OpsTide is watching')}</Link>}
                </span>
              </div>
            )}
            {[...byDay.entries()].map(([d, rs]) => (
              <div key={d} className="daygroup">
                <h2>{longDay(rs[0].at, tz)} <span className="muted" style={{ fontSize: 13, fontWeight: 400 }}>· {tn(rs.length, '{n} record', '{n} records')}</span></h2>
                {groupBy ? <Grouped rs={rs} rows={rows} tz={tz} /> : rows(rs)}
              </div>
            ))}
            {records.length > 0 && (
              <div style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap' }}>
                {cursor && <button type="button" className="button" disabled={more} onClick={loadMore}>{more ? t('Loading…') : t('Older records')}</button>}
                <button type="button" className="button" onClick={() => { const r = addToWorkspace(records.map((x) => x.id)); setAdded(r.full > 0 ? t('{added} added; {full} did not fit (the Workspace holds at most {max}).', { added: r.added, full: r.full, max: WORKSPACE_MAX }) : t('{added} added.', { added: r.added })) }}>{t('Add the {n} shown to the Workspace', { n: records.length })}</button>
                {added && <span className="muted" role="status">{added}</span>}
                {newerArrived && <span className="muted" role="status">{t('Newer records arrived; reload the page to see them.')}</span>}
                {!cursor && <span className="muted">{t('That is every record the filters match in this range.')}</span>}
              </div>
            )}
          </section>
          <aside aria-label={t('Record details')} className={`drawer${rec ? ' open' : ''}`}>
            {rec ? <RecordPanel key={rec} id={rec} tz={tz} close={() => set({ rec: null })} hrefOf={(x) => withParams(params, { rec: x })} /> : <div className="empty">{t('Pick a record to see where it came from, what it is connected to, and the record as stored.')}</div>}
          </aside>
        </div>
      )}
    </div>
  )
}

/** One row per exact resource identity, with the originals under it. */
function Grouped({ rs, rows, tz }: { rs: ExplorerRecord[]; rows: (r: ExplorerRecord[]) => React.ReactNode; tz: string }) {
  const groups = new Map<string, { label: string; rs: ExplorerRecord[] }>()
  for (const r of rs) {
    const id = identityOf(r)
    const g = groups.get(id.key) ?? { label: id.label, rs: [] }
    g.rs.push(r)
    groups.set(id.key, g)
  }
  return (
    <>
      {[...groups.entries()].map(([k, g]) => g.rs.length === 1 ? <div key={k}>{rows(g.rs)}</div> : (
        <details key={k} className="egroup">
          <summary>
            <span className="mono">{hhmm(g.rs[0].at, tz)}</span>
            <span><b>{g.label}</b> <span className="muted">· {t('{n} records, {from}–{to}', { n: g.rs.length, from: hhmm(g.rs[g.rs.length - 1].at, tz), to: hhmm(g.rs[0].at, tz) })}</span></span>
          </summary>
          {rows(g.rs)}
        </details>
      ))}
    </>
  )
}
