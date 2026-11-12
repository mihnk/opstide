import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { getJSON, query } from '../api'
import { Coverage } from '../components/Coverage'
import { AlertIcon, ArchiveIcon, FlagIcon, GapIcon, PlugIcon, SearchIcon } from '../components/icons'
import { Note } from '../components/Note'
import { Failed } from '../components/Shell'
import {
  emptyTitle, quickRanges, tokensOf, type EmptyKind, type ExplorerContext, type ExplorerFilters, type ExplorerHistogram, type ExplorerList, type ExplorerRecord,
} from '../explorer'
import { t, tn } from '../i18n'
import { useApi, useLive } from '../live'
import { Link, navigate, withParams } from '../router'
import { addToWorkspace, useWorkspace, WORKSPACE_MAX } from '../workspace'
import { RecordPanel } from './ExplorerPanel'
import { Facets, facetParams } from './ExplorerFacets'
import { Actions, FiltersButton, LiveToggle, SearchField, TimePicker, Toolbar } from './ExplorerQuery'
import { RecordTable } from './ExplorerTable'
import { Volume } from './ExplorerVolume'

// Explorer: everything OpsTide recorded, on one timeline. It shows evidence; it does not interpret it. Routine records
// are hidden by default and counted; nothing else is hidden. Filters live in the link (no token, no Event message).
// The layout is the one log explorers share: a query bar, a volume chart, facets on the left, the records, and the
// selected record in a drawer on the right.

const emptyIcon: Record<EmptyKind, typeof SearchIcon> = {
  no_matches: SearchIcon, source_unavailable: PlugIcon, outside_retention: ArchiveIcon, before_first_observation: FlagIcon, gap: GapIcon, collection_gap: GapIcon,
}

/** At this width and below the drawer is a sheet over the records (see styles.css). */
const overlay = () => typeof window !== 'undefined' && !!window.matchMedia && window.matchMedia('(max-width: 1280px)').matches

export function Explorer({ params, tz, setTz }: { params: URLSearchParams; tz: string; setTz: (z: string) => void }) {
  const get = (k: string) => params.get(k) ?? ''
  const custom = !!(params.get('from') || params.get('to'))
  const filter = {
    range: custom ? '' : get('range') || '24h', from: get('from'), to: get('to'), q: get('q'), namespace: get('namespace'), kind: get('kind'), name: get('name'), uid: get('uid'),
    workload: get('workload'), release: get('release'), run: get('run'), operation: get('operation'), node: get('node'), group: get('group'), type: get('type'),
    reason: get('reason'), source: get('source'), provenance: get('provenance'), severity: get('severity'), outcome: get('outcome'), owner: get('owner'),
    preset: get('preset'), routine: get('routine'), maintenance: get('maintenance'),
  }
  const rec = get('rec')
  const groupBy = get('by') === 'resource'
  const path = `/api/v1/explorer${query({ ...filter, limit: 100 })}`
  const { data, error } = useApi<ExplorerList>(path, { keep: true })
  const { data: facets } = useApi<ExplorerFilters>(`/api/v1/explorer/filters${query({ ...filter, routine: '' })}`, { keep: true })
  const { data: hist } = useApi<ExplorerHistogram>(`/api/v1/explorer/histogram${query({ ...filter, tz })}`, { keep: true })
  const { data: ctx } = useApi<ExplorerContext>(data ? `/api/v1/explorer/context${query({ from: data.from, to: data.to, namespace: filter.namespace })}` : null, { keep: true })
  const live = useLive()

  const paramsKey = params.toString()
  const set = useCallback((change: Record<string, string | null>) => navigate(withParams(new URLSearchParams(paramsKey), { rec: null, ...change }), true), [paramsKey])
  const searchRef = useRef<HTMLInputElement>(null)
  const [sheet, setSheet] = useState(false)

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

  const open = (id: string) => {
    navigate(withParams(params, { rec: id }), true)
    // Where the drawer covers the records it takes the focus; beside them, the keyboard stays in the table.
    if (overlay()) window.setTimeout(() => document.getElementById('record-title')?.focus(), 60)
  }
  const close = useCallback(() => {
    const was = new URLSearchParams(location.search).get('rec')
    navigate(withParams(new URLSearchParams(location.search), { rec: null }), true)
    if (was) window.setTimeout(() => document.querySelector<HTMLElement>(`[data-row="${CSS.escape(was)}"]`)?.focus(), 30)
  }, [])

  // The filter sheet of a narrow screen takes the focus, and Escape closes it.
  useEffect(() => {
    if (!sheet) return
    document.querySelector<HTMLInputElement>('#ex-facets .fx-search input')?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopImmediatePropagation()
      setSheet(false)
      document.querySelector<HTMLElement>('.filters-btn')?.focus()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [sheet])

  useEffect(() => {
    if (!rec) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') close()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [rec, close])

  const frozen = older.base === path ? older.head : undefined
  const newerArrived = !!frozen && !!data && data.records.some((r) => !frozen.some((f) => f.id === r.id) && r.at >= (frozen[0]?.at ?? ''))
  const liveRecords = data?.records
  const records = useMemo(() => {
    const first = (older.base === path ? older.head : undefined) ?? liveRecords ?? []
    const seen = new Set(first.map((r) => r.id))
    return [...first, ...(older.base === path ? older.records : []).filter((r) => !seen.has(r.id))]
  }, [older, path, liveRecords])

  const tokens = tokensOf(params)
  const filtered = tokens.length > 0 || !!filter.q
  const clear: Record<string, null> = { q: null, routine: null }
  for (const k of facetParams) clear[k] = null
  const clearAll = withParams(params, { ...clear, rec: null })
  const wsIds = useWorkspace()
  const [added, setAdded] = useState('')

  // Live: the range follows now. Pausing pins the range on screen; going live again takes the quick range of the same
  // length (or the last 24 hours).
  const following = !custom
  const toggleLive = () => {
    if (following) {
      if (data) set({ range: null, from: data.from.replace(/\.\d{3}Z$/, 'Z'), to: data.to.replace(/\.\d{3}Z$/, 'Z') })
      return
    }
    const span = data ? Date.parse(data.to) - Date.parse(data.from) : 0
    const lengths: Record<string, number> = { '15m': 15 * 60e3, '1h': 3600e3, '6h': 6 * 3600e3, '24h': 24 * 3600e3, '7d': 7 * 86400e3 }
    const r = quickRanges.find((x) => Math.abs(lengths[x] - span) < 60e3)
    set({ from: null, to: null, range: r && r !== '24h' ? r : null })
  }

  const scopes = new Map((ctx?.scopes ?? []).map((s) => [s.scope, s.label]))
  const scopeLabel = (s: string) => t(scopes.get(s) ?? s)
  const exportFilter = Object.fromEntries(Object.entries(filter).filter(([, v]) => v)) as Record<string, string>
  const count = hist?.total
  const notices = data?.notices ?? []

  return (
    <div className={`ex${rec ? ' has-rec' : ''}${sheet ? ' sheet' : ''}`}>
      <h1 className="sr">{t('Explorer')}: {t('What OpsTide recorded')}</h1>
      <Toolbar>
        <FiltersButton n={tokens.length} open={sheet} onClick={() => setSheet(!sheet)} />
        <SearchField params={params} set={set} inputRef={searchRef} />
        <TimePicker params={params} set={set} tz={tz} setTz={setTz} from={data?.from} to={data?.to} />
        <LiveToggle live={following} connected={live.connected} onToggle={toggleLive} />
        <span className="ex-div" aria-hidden="true" />
        <Actions wsCount={wsIds.length} filterQuery={exportFilter} />
      </Toolbar>

      <div className="ex-body">
        <div id="ex-facets" className="ex-side">
          <Facets params={params} facets={facets} data={data} set={set} onClose={sheet ? () => setSheet(false) : undefined} />
        </div>
        {sheet && <button type="button" className="ex-scrim" aria-label={t('Close the filters')} onClick={() => setSheet(false)} />}

        <section className="ex-results" aria-label={t('Timeline')}>
          {hist && (
            <Volume hist={hist} ctx={ctx} tz={tz} live={following} onNarrow={(from, to) => set({ range: null, from, to })}
              head={
                <span className="vol-count">
                  <b><span className="mono">{count ?? '…'}</span> {tn(count ?? 0, 'record', 'records')}</b>
                  {data && filter.routine !== '1' && data.suppressedRoutine > 0 && (
                    <button type="button" className="linkbtn quiet" onClick={() => set({ routine: '1' })} title={t('Left out of the timeline, not deleted')}>{t('{n} routine hidden', { n: data.suppressedRoutine })}</button>
                  )}
                  {data && filter.maintenance === 'outside' && (data.suppressedMaintenance ?? 0) > 0 && <span className="muted">{t('{n} in maintenance left out', { n: data.suppressedMaintenance ?? 0 })}</span>}
                </span>
              } />
          )}
          {(notices.length > 0 || ctx) && (
            <div className={`ex-cov${notices.length ? ' gap' : ''}`}>
              {notices.length > 0 && (
                <div className="ex-notice" role="note">
                  <i className="hatch-sw" aria-hidden="true" />
                  <span><b>{t('Not whole')}</b> {notices.map((n, i) => <span key={i}>{n} </span>)}</span>
                </div>
              )}
              {ctx && <Coverage ctx={ctx} tz={tz} compact />}
            </div>
          )}

          {error && !data && <div className="ex-pad"><Failed error={error} /></div>}
          {!data && !error && <div className="ex-pad muted" aria-busy="true">{t('Loading…')}</div>}
          {data && records.length === 0 && data.empty && (
            <Empty kind={data.empty.kind} message={data.empty.message} detail={data.empty.detail}>
              {filtered && <Link href={clearAll} replace>{t('Clear the filters')}</Link>}
              {data.suppressedRoutine > 0 && filter.routine !== '1' && <Link href={withParams(params, { routine: '1' })} replace>{tn(data.suppressedRoutine, 'Show {n} routine record', 'Show {n} routine records')}</Link>}
              {!following && <button type="button" className="linkbtn" onClick={() => set({ from: null, to: null, range: null })}>{t('Back to the last 24 hours')}</button>}
              {data.empty.kind !== 'no_matches' && <Link href="/system">{t('See what OpsTide is watching')}</Link>}
            </Empty>
          )}
          {data && records.length > 0 && (
            <>
              <RecordTable records={records} tz={tz} rec={rec} onOpen={open} groupBy={groupBy} scopeLabel={scopeLabel} label={t('Records')} />
              <div className="ex-foot">
                {cursor && <button type="button" className="button" disabled={more} onClick={loadMore}>{more ? t('Loading…') : t('Older records')}</button>}
                <button type="button" className="button quiet" onClick={() => { const r = addToWorkspace(records.map((x) => x.id)); setAdded(r.full > 0 ? t('{added} added; {full} did not fit (the Workspace holds at most {max}).', { added: r.added, full: r.full, max: WORKSPACE_MAX }) : t('{added} added.', { added: r.added })) }}>{t('Add the {n} shown to the Workspace', { n: records.length })}</button>
                {added && <span className="muted" role="status">{added}</span>}
                {newerArrived && <span className="muted" role="status">{t('Newer records arrived; reload the page to see them.')}</span>}
                {!cursor && <span className="muted">{t('That is every record the filters match in this range.')}</span>}
                <span className="grow" />
                <span className="muted sm">{t('newest first')} · <Note more={data.orderBasis}>{t('record time order')}</Note></span>
              </div>
            </>
          )}
        </section>

        {rec && (
          <aside className="ex-drawer" aria-label={t('Record details')}>
            <RecordPanel key={rec} id={rec} tz={tz} close={close} hrefOf={(x) => withParams(params, { rec: x })} />
          </aside>
        )}
      </div>
    </div>
  )
}

function Empty({ kind, message, detail, children }: { kind: EmptyKind; message: string; detail: string[]; children: React.ReactNode }) {
  const Icon = emptyIcon[kind] ?? AlertIcon
  return (
    <div className={`ex-empty ${kind}`} role="status">
      <span className="ex-empty-icon"><Icon size={22} /></span>
      <div className="ex-empty-text">
        <span className="label">{emptyTitle[kind]}</span>
        <b>{message}</b>
        {detail.map((d, i) => <span key={i} className="muted">{d}</span>)}
        <span className="ex-empty-actions">{children}</span>
      </div>
    </div>
  )
}
