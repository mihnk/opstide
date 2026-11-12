import { useRef, useState, type FocusEvent, type KeyboardEvent, type ReactNode } from 'react'
import { GapIcon, RecordIcon, ChevronRight } from '../components/icons'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import { classOf, groupShort, provWord, type ExplorerRecord } from '../explorer'
import { t, tn } from '../i18n'
import { hhmm, hhmmss, isoDate, longDay } from '../time'

// The Explorer's record table, and the cells the Workspace draws its records with. One row per record, newest first:
// a severity strip, the time, the kind, what happened, the resource and how it is known. A record that says a watch
// was not whole is drawn as a hatched row across the table, so a quiet stretch is never read as a calm one.

/** How a record is known, as a chip: a solid border is observed, dashed derived, dotted unknown. */
export function ProvChip({ p, title }: { p: string; title?: string }) {
  return <span className={`pchip prov-${p.toLowerCase()}`} title={title}>{provWord(p)}</span>
}

/** The kind of record, as its icon in the colour of its class. */
export function KindMark({ r, size = 18 }: { r: ExplorerRecord; size?: number }) {
  return <RecordIcon type={r.type} group={r.group} size={size} className={`kind k-${classOf(r)}`} label={r.group === 'events' ? `Kubernetes Event${r.eventType ? ` (${r.eventType})` : ''}` : groupShort[r.group] ?? r.group} />
}

/** The resource a record is about, as namespace/name. */
export function resourceText(r: ExplorerRecord): string {
  const x = r.resource
  return x.name ? `${x.namespace ? `${x.namespace}/` : ''}${x.name}` : ''
}

/** What happened: a title (a Kubernetes Event's reason, else the summary) with its detail, and a mono line with the type. */
export function EventCell({ r, extra }: { r: ExplorerRecord; extra?: ReactNode }) {
  const ev = r.type === 'kubernetes.event'
  const title = ev && r.reason ? r.reason : r.summary
  const detail = ev ? `${r.resource.kind} ${r.resource.name ?? ''}`.trim() : r.reason && !r.summary.includes(r.reason) ? r.reason : ''
  const sub = [ev ? `${r.eventType ?? 'Event'} · kubernetes.event` : r.type, r.count && r.count.value > 1 ? `×${r.count.value}` : ''].filter(Boolean).join(' · ')
  return (
    <span className="ev">
      <span className="ev-title" title={r.summary}>{title}{detail && <span className="ev-detail"> · {detail}</span>}</span>
      <span className="ev-sub mono">{sub}{extra}</span>
    </span>
  )
}

function MaintChip({ r }: { r: ExplorerRecord }) {
  if (!r.maintenance) return null
  return <span className="chip maint" title={`${r.maintenance.statement} · ${provWord(r.maintenance.provenance)}`}>{t('maintenance: {name}', { name: r.maintenance.name })}</span>
}

/** Identity used to group: the UID when the record has one, else kind, namespace and name. Never a guess. */
function identityOf(r: ExplorerRecord): { key: string; label: string } {
  const x = r.resource
  if (!x.kind && !x.name && !x.uid) return { key: `~${r.id}`, label: t('no resource named') }
  const key = x.uid ? `${x.kind}|${x.uid}` : `${x.kind}|${x.namespace ?? ''}|${x.name ?? ''}`
  return { key, label: `${x.kind || t('Resource')} ${x.namespace ? `${x.namespace}/` : ''}${x.name ?? x.uid ?? ''}` }
}

interface RowProps { r: ExplorerRecord; tz: string; selected: boolean; active: boolean; onOpen: (id: string) => void; onFocus: (id: string) => void; scopeLabel: (s: string) => string; nested?: boolean }

function Row({ r, tz, selected, active, onOpen, onFocus, scopeLabel, nested }: RowProps) {
  const cls = classOf(r)
  const common = {
    role: 'row', 'aria-selected': selected, tabIndex: active ? 0 : -1, 'data-row': r.id,
    onClick: () => onOpen(r.id),
    onFocus: (e: FocusEvent) => { if (e.target === e.currentTarget) onFocus(r.id) },
    onKeyDown: (e: KeyboardEvent) => {
      if (e.target !== e.currentTarget) return
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onOpen(r.id) }
    },
  } as const
  if (r.gap) {
    return (
      <div {...common} className={`ex-row gaprow${nested ? ' nested' : ''}`}>
        <span role="gridcell" className="gap-cell">
          <GapIcon size={16} />
          <span className="mono">{hhmm(r.gap.from, tz)} – {hhmm(r.gap.to, tz)}</span>
          <span>{t('{scope} not watched — records from this stretch may be missing', { scope: scopeLabel(r.gap.scope || r.resource.kind) })}</span>
          <MaintChip r={r} />
        </span>
      </div>
    )
  }
  return (
    <div {...common} className={`ex-row k-${cls}${nested ? ' nested' : ''}`}>
      <span className="sevstrip" aria-hidden="true" />
      <span role="gridcell" className="when mono">{hhmmss(r.at, tz)}</span>
      <span role="gridcell" className="kindcell"><KindMark r={r} /></span>
      <span role="gridcell" className="evcell"><EventCell r={r} extra={r.maintenance ? <> <MaintChip r={r} /></> : null} /></span>
      <span role="gridcell" className="res mono" title={r.resource.name ? `${r.resource.kind} ${resourceText(r)}` : undefined}>{resourceText(r) || <span className="muted">—</span>}</span>
      <span role="gridcell"><ProvChip p={r.provenance} title={r.source} /></span>
      <span role="gridcell" className="wscell"><WorkspaceToggle id={r.id} compact /></span>
    </div>
  )
}

/** The rows of one resource, under a row that opens and closes them. */
function Group({ label, rs, tz, render }: { label: string; rs: ExplorerRecord[]; tz: string; render: (r: ExplorerRecord) => ReactNode }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <div role="row" className="ex-row grouprow">
        <span role="gridcell" className="group-cell">
          <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} data-row-btn="">
            <ChevronRight size={14} className={open ? 'turned' : undefined} />
            <span className="mono">{hhmm(rs[rs.length - 1].at, tz)}–{hhmm(rs[0].at, tz)}</span>
            <b>{label}</b>
            <span className="muted">{tn(rs.length, '{n} record', '{n} records')}</span>
          </button>
        </span>
      </div>
      {open && rs.map(render)}
    </>
  )
}

export function RecordTable({ records, tz, rec, onOpen, groupBy, scopeLabel, label }: {
  records: ExplorerRecord[]
  tz: string
  rec: string
  onOpen: (id: string) => void
  groupBy?: boolean
  scopeLabel: (scope: string) => string
  label: string
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [focusId, setFocusId] = useState('')
  const active = records.some((r) => r.id === focusId) ? focusId : records.some((r) => r.id === rec) ? rec : records[0]?.id

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) return
    const rows = [...(ref.current?.querySelectorAll<HTMLElement>('[data-row], [data-row-btn]') ?? [])]
    const cur = rows.findIndex((x) => x === document.activeElement || x.contains(document.activeElement))
    if (cur < 0 && !rows.length) return
    const next = e.key === 'Home' ? 0 : e.key === 'End' ? rows.length - 1 : Math.min(rows.length - 1, Math.max(0, cur + (e.key === 'ArrowDown' ? 1 : -1)))
    e.preventDefault()
    rows[next]?.focus()
  }

  const row = (r: ExplorerRecord, nested?: boolean) => (
    <Row key={r.id} r={r} tz={tz} selected={r.id === rec} active={r.id === active} onOpen={onOpen} onFocus={setFocusId} scopeLabel={scopeLabel} nested={nested} />
  )

  const byDay = new Map<string, ExplorerRecord[]>()
  for (const r of records) byDay.set(isoDate(r.at, tz), [...(byDay.get(isoDate(r.at, tz)) ?? []), r])

  const body = (rs: ExplorerRecord[]) => {
    if (!groupBy) return rs.map((r) => row(r))
    const groups = new Map<string, { label: string; rs: ExplorerRecord[] }>()
    for (const r of rs) {
      const id = identityOf(r)
      const g = groups.get(id.key) ?? { label: id.label, rs: [] }
      g.rs.push(r)
      groups.set(id.key, g)
    }
    return [...groups.entries()].map(([k, g]) => g.rs.length === 1 ? row(g.rs[0]) : <Group key={k} label={g.label} rs={g.rs} tz={tz} render={(r) => row(r, true)} />)
  }

  return (
    <div ref={ref} role="grid" aria-label={label} className="ex-grid" onKeyDown={onKey}>
      <div role="row" className="ex-row head">
        <span aria-hidden="true" />
        <span role="columnheader" aria-sort="descending" className="sorted">{t('Time')} <span aria-hidden="true">▾</span></span>
        <span role="columnheader"><span className="sr">{t('Kind')}</span></span>
        <span role="columnheader">{t('Event')}</span>
        <span role="columnheader">{t('Resource')}</span>
        <span role="columnheader">{t('Provenance')}</span>
        <span role="columnheader"><span className="sr">{t('Workspace')}</span></span>
      </div>
      {[...byDay.entries()].map(([d, rs], i) => (
        <div key={d} role="rowgroup" aria-label={longDay(rs[0].at, tz)}>
          {(byDay.size > 1 || i > 0) && (
            <div role="row" className="ex-row dayrow">
              <span role="gridcell"><h2 className="day">{longDay(rs[0].at, tz)} <span className="muted">· {tn(rs.length, '{n} record', '{n} records')}</span></h2></span>
            </div>
          )}
          {body(rs)}
        </div>
      ))}
    </div>
  )
}
