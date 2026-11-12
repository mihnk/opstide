import { useState, type ReactNode } from 'react'
import { ChevronRight, CloseIcon, GroupIcon, SearchIcon } from '../components/icons'
import { groupOrder, groupShort, provWord, tokenKeys, withoutToken, withToken, type ExplorerFilters, type ExplorerList, type FilterChoice } from '../explorer'
import { locale, t, tn } from '../i18n'

// The facet sidebar: every filter the Explorer has, as lists of values with how many records each would match (each
// count leaves out its own filter, so the other values can still be picked). A value no record carries is muted and
// cannot be picked. A search box narrows the lists. Choosing writes the filter into the link, like every other control.

interface Val {
  value: string
  name: ReactNode
  /** What the search box matches. */
  text: string
  count?: number
  lead?: ReactNode
  bar?: { pct: number; cls: string }
  mono?: boolean
}

interface Grp {
  id: string
  title: string
  param: string
  values: Val[]
  /** How many are shown before "N more". */
  top?: number
}

const lower = (s: string) => s.toLocaleLowerCase(locale())

export const facetParams = tokenKeys.map((k) => k.param)

function Marker({ cls }: { cls: string }) {
  return <i className={`mk mk-${cls}`} aria-hidden="true" />
}

function choices(list: FilterChoice[] | undefined, current: string, extra?: (c: FilterChoice) => Partial<Val>): Val[] {
  const out: Val[] = (list ?? []).map((c) => ({ value: c.value, name: c.value, text: c.value, count: c.count, mono: true, ...extra?.(c) }))
  for (const v of current.split(',').filter(Boolean)) if (!out.some((x) => x.value === v)) out.unshift({ value: v, name: v, text: v, count: 0, mono: true })
  return out
}

export function Facets({ params, facets, data, set, onClose }: {
  params: URLSearchParams
  facets?: ExplorerFilters
  data?: ExplorerList
  set: (change: Record<string, string | null>) => void
  /** Where the sidebar is a sheet over the page (narrow screens), the way to close it. */
  onClose?: () => void
}) {
  const [find, setFind] = useState('')
  const [open, setOpen] = useState<Record<string, boolean>>({})
  const [all, setAll] = useState<Record<string, boolean>>({})
  const get = (k: string) => params.get(k) ?? ''
  const list = (k: string) => get(k).split(',').filter(Boolean)

  const outcomeSum = Math.max(1, ...[(facets?.outcomes ?? []).reduce((n, c) => n + c.count, 0)])
  const sevSum = Math.max(1, (facets?.severity ?? []).reduce((n, c) => n + c.count, 0))
  const groups: Grp[] = [
    {
      id: 'group', title: t('Record type'), param: 'group',
      values: groupOrder.map((g) => ({ value: g, name: groupShort[g], text: `${groupShort[g]} ${g}`, count: facets ? facets.groups.find((x) => x.value === g)?.count ?? 0 : undefined, lead: <GroupIcon group={g} size={16} /> })),
    },
    {
      id: 'outcome', title: t('Run outcome'), param: 'outcome',
      values: ([['failed', t('Failed'), 'fail'], ['succeeded', t('Succeeded'), 'run'], ['unknown', t('Unknown'), 'unknown']] as const).map(([v, name, cls]) => {
        const count = facets?.outcomes?.find((x) => x.value === v)?.count
        return { value: v, name, text: `${name} ${v}`, count, lead: <Marker cls={cls} />, bar: count !== undefined ? { pct: (count / outcomeSum) * 100, cls } : undefined }
      }),
    },
    {
      id: 'severity', title: t('Severity'), param: 'severity',
      values: ([['error', t('Errors'), 'fail'], ['warning', t('Warnings'), 'warn'], ['info', t('Information'), 'info']] as const).map(([v, name, cls]) => {
        const count = facets?.severity?.find((x) => x.value === v)?.count
        return { value: v, name, text: `${name} ${v}`, count, lead: <Marker cls={cls} />, bar: count !== undefined ? { pct: (count / sevSum) * 100, cls } : undefined }
      }),
    },
    { id: 'namespace', title: t('Namespace'), param: 'namespace', values: choices(facets?.namespaces, get('namespace')), top: 5 },
    { id: 'kind', title: t('Resource kind'), param: 'kind', values: choices(facets?.kinds, get('kind')), top: 6 },
    {
      id: 'provenance', title: t('Provenance'), param: 'provenance',
      values: ['observed', 'derived', 'unknown'].map((p) => ({ value: p, name: provWord(p), text: `${provWord(p)} ${p}`, count: facets ? facets.provenance.find((x) => x.value === p)?.count ?? 0 : undefined, lead: <Marker cls={`prov-${p}`} /> })),
    },
    { id: 'reason', title: t('Reason'), param: 'reason', values: choices(facets?.reasons, get('reason')), top: 6 },
    { id: 'owner', title: t('Owner'), param: 'owner', values: choices(facets?.owners, get('owner')), top: 6 },
    { id: 'source', title: t('Source'), param: 'source', values: (facets?.sources ?? []).map((s) => ({ value: s, name: s, text: s, mono: true })) },
  ]

  const q = lower(find.trim())
  const isOn = (g: Grp, v: string) => list(g.param).includes(v)
  const toggle = (g: Grp, v: string) => set(isOn(g, v) ? withoutToken(params, g.param, v) : withToken(params, g.param, v))
  const active = facetParams.filter((k) => get(k)).length + (get('q') ? 1 : 0)
  const defaultOpen = (g: Grp) => ['group', 'outcome', 'namespace'].includes(g.id) || list(g.param).length > 0
  const isOpen = (id: string, dflt: boolean) => open[id] ?? dflt

  const clear: Record<string, null> = { q: null }
  for (const k of facetParams) clear[k] = null

  const sw = (on: boolean, change: Record<string, string | null>, label: ReactNode, note?: string) => (
    <button type="button" role="switch" aria-checked={on} className="fx-switch" title={note} onClick={() => set(change)}>
      <span>{label}</span><i aria-hidden="true" />
    </button>
  )
  const routine = get('routine') === '1'
  const preset = get('preset') === 'changes'
  const outside = get('maintenance') === 'outside'
  const byRes = get('by') === 'resource'

  let shownAny = false
  return (
    <aside className="fx" aria-label={t('Filters')}>
      <div className="fx-head">
        <h2 className="label">{t('Filters')}</h2>
        <span className="grow" />
        <button type="button" className="linkbtn" disabled={active === 0} onClick={() => set(clear)}>{t('Clear')}</button>
        {onClose && <button type="button" className="ibtn small" aria-label={t('Close the filters')} onClick={onClose}><CloseIcon size={16} /></button>}
      </div>
      <label className="fx-search">
        <SearchIcon size={14} />
        <input type="search" value={find} onChange={(e) => setFind(e.target.value)} placeholder={t('Search filters')} aria-label={t('Search filters')} />
      </label>

      {groups.map((g) => {
        const vals = q ? g.values.filter((v) => lower(v.text).includes(q)) : g.values
        if (q && vals.length === 0) return null
        if (!q && g.values.length === 0 && !facets) return null
        shownAny = true
        const chosen = list(g.param).length
        const expanded = !!all[g.id] || !!q
        const cut = g.top && !expanded ? Math.max(g.top, vals.findLastIndex((v) => isOn(g, v.value)) + 1) : vals.length
        const opened = q ? true : isOpen(g.id, defaultOpen(g))
        return (
          <details key={g.id} className="fx-group" open={opened} onToggle={(e) => { if (!q) { const o = e.currentTarget.open; setOpen((s) => (s[g.id] === o ? s : { ...s, [g.id]: o })) } }}>
            <summary>
              <ChevronRight size={12} className="chev" />
              <span>{g.title}</span>
              {chosen > 0 ? <span className="fx-on mono" aria-label={tn(chosen, '{n} chosen', '{n} chosen')}>{chosen}</span> : <span className="fx-n mono" aria-hidden="true">{g.values.length || ''}</span>}
            </summary>
            <div className="fx-vals" role="group" aria-label={g.title}>
              {vals.length === 0 && <span className="fx-none">{t('No values in this range')}</span>}
              {vals.slice(0, cut).map((v) => {
                const on = isOn(g, v.value)
                const off = v.count === 0 && !on
                return (
                  <label key={v.value} className={`fx-val${on ? ' on' : ''}${off ? ' zero' : ''}${v.lead ? ' lead' : ''}`}>
                    <input type="checkbox" checked={on} disabled={off} onChange={() => toggle(g, v.value)} />
                    {v.lead}
                    <span className="fx-name">
                      <span className={v.mono ? 'mono' : undefined}>{v.name}</span>
                      {v.bar && <span className={`fx-bar ${v.bar.cls}`} aria-hidden="true"><i style={{ width: `${v.bar.pct}%` }} /></span>}
                    </span>
                    <span className="fx-count mono">{v.count ?? ''}</span>
                  </label>
                )
              })}
              {g.top && !q && vals.length > g.top && (
                <button type="button" className="linkbtn fx-more" aria-expanded={expanded} onClick={() => setAll((s) => ({ ...s, [g.id]: !expanded }))}>
                  {expanded ? t('Show fewer') : t('+ {n} more', { n: vals.length - cut })}
                </button>
              )}
            </div>
          </details>
        )
      })}
      {q && !shownAny && <p className="fx-none">{t('No filter value matches “{text}”.', { text: find.trim() })}</p>}

      <details className="fx-group" open={isOpen('display', true)} onToggle={(e) => { const o = e.currentTarget.open; setOpen((s) => (s.display === o ? s : { ...s, display: o })) }}>
        <summary><ChevronRight size={12} className="chev" /><span>{t('Display')}</span></summary>
        <div className="fx-switches">
          {sw(preset, { preset: preset ? null : 'changes' }, t('Changes only'), t('Only records that say something was changed: a spec written (and by which client), a revision, a replica target, a schedule or suspension, a cordon'))}
          {sw(routine, { routine: routine ? null : '1' }, <>{t('Routine records')}{!routine && data && data.suppressedRoutine > 0 && <span className="mono muted"> +{data.suppressedRoutine}</span>}</>,
            t('Pod created, ready and terminated, Normal Events, Job creation and reported counts. Left out of the timeline, not deleted.'))}
          {sw(outside, { maintenance: outside ? null : 'outside' }, <>{t('Outside maintenance only')}{outside && (data?.suppressedMaintenance ?? 0) > 0 && <span className="mono muted"> −{data?.suppressedMaintenance}</span>}</>,
            t('Leaves out the records inside a maintenance window declared in the configuration. They are still recorded, and shown when this is off.'))}
          {sw(byRes, { by: byRes ? null : 'resource' }, t('Group by resource'))}
        </div>
      </details>
    </aside>
  )
}
