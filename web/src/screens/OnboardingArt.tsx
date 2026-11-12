import type { ReactNode } from 'react'
import { query, type Mark, type Operations, type OperationRow, type Range, type System, type CellState } from '../api'
import { DurationCell, LongerMark } from '../components/Duration'
import { Strip } from '../components/Strip'
import { Legend, Timeline, type TimelineLane } from '../components/Timeline'
import { provWord, type BeforeChange, type ExplorerBefore, type ExplorerList, type ExplorerRecord } from '../explorer'
import { locale, t } from '../i18n'
import { kindName } from '../kinds'
import { useApi } from '../live'
import { exceptionText } from '../narrative'
import { hhmm, shortDay } from '../time'
import { rangeQuery } from '../window'

// The pictures of the guide. Where a screen is shown, it is drawn with the components that screen uses, fed from this
// cluster's record when there is something to show, and from a small fixed example when there is not (a new install).
// Every picture is inert: it shows, it does not take focus or clicks. The figure says which it is: live or example.

export function Figure({ live, children, wide, title }: { live?: boolean; children: ReactNode; wide?: boolean; title?: string }) {
  return (
    <figure className={`ob-figure${wide ? ' wide' : ''}`}>
      <figcaption className="ob-cap">
        {live ? <><LiveDot />{t('Live from this cluster')}</> : <><ExampleDot />{t('Example')}</>}
        {title && <span className="ob-cap-title">{title}</span>}
      </figcaption>
      <div className="ob-art" inert>{children}</div>
    </figure>
  )
}

function LiveDot() {
  return <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><circle cx="5" cy="5" r="4" fill="var(--run)" /></svg>
}
function ExampleDot() {
  return <svg width="10" height="10" viewBox="0 0 10 10" aria-hidden="true"><circle cx="5" cy="5" r="3.5" fill="none" stroke="var(--muted)" strokeWidth="1.4" /></svg>
}

/** A numbered marker, in the picture and in the list beside it. */
export function Pin({ n }: { n: number }) {
  return <span className="pin mono" aria-hidden="true">{n}</span>
}

function Block({ n, children }: { n: number; children: ReactNode }) {
  return <div className="mini-block"><Pin n={n} /><div className="mini-body">{children}</div></div>
}

// ── step 1: a first look ──────────────────────────────

/** A day on the Overview, drawn with the Overview's own timetable from the fixed example (no request): it changes with the
 *  language and theme picked above it, so the choice can be judged on the real thing. */
export function PreviewArt() {
  const s = sampleRange()
  const lanes = s.lanes.map((l) => ({ ...l, maintenance: undefined }))
  return (
    <Figure title={t('A day on the Overview')}>
      <div className="ob-tl ob-preview">
        <Timeline label={t('Operation')} from={s.from} to={s.to} tz="UTC" now={s.now} gaps={s.gaps} lanes={lanes} compact labelWidth={140} />
      </div>
      <div className="ob-preview-foot">
        <Legend items={['succeeded', 'failed', 'expected']} />
        <div className="mini-rows">
          <span className="label">{t('Exceptions')}</span>
          {s.exceptions.map((e, i) => (
            <span key={i} className="mini-row"><span className={`mono${e.red ? ' fail' : ' muted'}`}>{e.when}</span><span>{e.title}</span></span>
          ))}
        </div>
      </div>
    </Figure>
  )
}

// ── step 2: what it is ────────────────────────────────

export function RecordArt() {
  return (
    <svg viewBox="0 0 160 96" className="ob-svg" aria-hidden="true">
      {[24, 48, 72].map((y) => <line key={y} x1="6" x2="154" y1={y} y2={y} stroke="var(--rule)" strokeWidth="1" />)}
      <line x1="6" x2="154" y1="88" y2="88" stroke="var(--ink)" strokeWidth="1.5" />
      {[[14, 16, 6], [40, 16, 10], [72, 16, 6], [104, 16, 6]].map(([x, y, w], i) => <rect key={i} x={x} y={y} width={w} height="14" fill="var(--run)" />)}
      {[[22, 40, 18], [70, 40, 22]].map(([x, y, w], i) => <rect key={i} x={x} y={y} width={w} height="14" fill="var(--run)" />)}
      <rect x="96" y="40" width="10" height="14" fill="var(--fail)" />
      <rect x="30" y="64" width="8" height="14" fill="var(--run)" />
      <rect x="84" y="64" width="8" height="14" fill="none" stroke="var(--accent)" strokeWidth="2" />
      <line x1="126" x2="126" y1="8" y2="92" stroke="var(--ink)" strokeWidth="2" />
      <rect x="134" y="64" width="8" height="14" fill="none" stroke="var(--hatch)" strokeWidth="1" />
    </svg>
  )
}

export function ReadOnlyArt() {
  return (
    <svg viewBox="0 0 160 96" className="ob-svg" aria-hidden="true">
      <rect x="6" y="8" width="116" height="80" rx="8" fill="none" stroke="var(--ink)" strokeWidth="1.5" strokeDasharray="5 4" />
      {[[18, 22], [18, 44], [18, 66]].map(([x, y], i) => <rect key={i} x={x} y={y} width="26" height="14" fill="none" stroke="var(--muted)" strokeWidth="1.4" />)}
      <rect x="78" y="36" width="34" height="26" fill="var(--ink)" />
      <path d="M86 49c3-5 15-5 18 0-3 5-15 5-18 0z" fill="none" stroke="var(--bg)" strokeWidth="1.4" />
      <circle cx="95" cy="49" r="2.2" fill="var(--bg)" />
      {[29, 51, 73].map((y) => <path key={y} d={`M46 ${y}L74 49`} stroke="var(--muted)" strokeWidth="1.2" strokeDasharray="3 3" />)}
      <path d="M112 49h34" stroke="var(--ink)" strokeWidth="1.5" />
      <path d="M140 44l6 5-6 5" fill="none" stroke="var(--ink)" strokeWidth="1.5" />
      <circle cx="130" cy="49" r="8" fill="var(--bg)" stroke="var(--fail)" strokeWidth="1.8" />
      <path d="M124.5 54.5l11-11" stroke="var(--fail)" strokeWidth="1.8" />
    </svg>
  )
}

export function NoCauseArt() {
  return (
    <svg viewBox="0 0 160 96" className="ob-svg" aria-hidden="true">
      <line x1="6" x2="154" y1="78" y2="78" stroke="var(--ink)" strokeWidth="1.5" />
      <rect x="18" y="56" width="16" height="16" fill="none" stroke="var(--ink)" strokeWidth="1.6" />
      <rect x="58" y="56" width="16" height="16" fill="none" stroke="var(--ink)" strokeWidth="1.6" />
      <path d="M130 52l12 12-12 12-12-12z" fill="var(--fail)" />
      <path d="M126 60l8 8M134 60l-8 8" stroke="var(--bg)" strokeWidth="1.6" />
      <path d="M26 54C40 18 104 14 128 50" fill="none" stroke="var(--muted)" strokeWidth="1.4" strokeDasharray="3 4" />
      <path d="M66 54C78 34 110 32 124 50" fill="none" stroke="var(--muted)" strokeWidth="1.4" strokeDasharray="3 4" />
      <circle cx="84" cy="24" r="11" fill="var(--bg)" stroke="var(--ink)" strokeWidth="1.5" />
      <text x="84" y="29" textAnchor="middle" fontSize="15" fontWeight="700" fill="var(--ink)" fontFamily="'Archivo Variable', sans-serif">?</text>
    </svg>
  )
}

// ── step 3: provenance ────────────────────────────────

export function ProvenanceArt() {
  const rows: [string, string, string, string][] = [
    ['OBSERVED', 'Reported by Kubernetes, or seen by OpsTide.', '03:15', 'Job data/nightly-backup failed: BackoffLimitExceeded'],
    ['DERIVED', 'Worked out from records by a fixed rule.', '10:00', 'Reconcile ledger did not start'],
    ['UNKNOWN', 'Not seen. OpsTide says so; it never guesses.', '06:00', 'Sales report: not confirmed, collection gap'],
  ]
  return (
    <div className="prov-rows">
      {rows.map(([p, what, at, ex]) => (
        <div key={p} className="prov-row">
          <span className={`chip prov-${p.toLowerCase()}`}>{provWord(p)}</span>
          <span className="prov-what">{t(what)}</span>
          <span className="prov-ex"><span className="mono">{at}</span> {ex}</span>
        </div>
      ))}
    </div>
  )
}

// ── step 4: gaps ──────────────────────────────────────

const exDay = '2026-03-02'
const at = (h: number, m = 0) => `${exDay}T${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:00Z`
const exMarks = (hours: number[], state: Mark['state'], len = 8): Mark[] => hours.map((h) => ({ start: at(h), end: at(h, len), state }))

export function GapsArt() {
  const lanes: TimelineLane[] = [
    { key: 'a', name: 'Reconcile ledger', meta: 'payments', marks: [...exMarks([0, 2, 4, 10], 'succeeded', 6), ...exMarks([6, 8], 'unconfirmed', 6)] },
    { key: 'b', name: 'Sales report', meta: 'data', marks: [...exMarks([0], 'succeeded', 10), ...exMarks([6], 'unconfirmed', 10)] },
    { key: 'c', name: 'Nightly backup', meta: 'data', marks: [{ start: at(3, 15), end: at(3, 40), state: 'failed' }] },
  ]
  return (
    <div className="ob-tl">
      <Timeline label={t('Operation')} from={at(0)} to={at(12)} tz="UTC" gaps={[{ from: at(5, 20), to: at(8, 40) }]} lanes={lanes} compact labelWidth={150} />
      <Legend items={['succeeded', 'failed', 'gap']} />
    </div>
  )
}

// ── step 5: connections ───────────────────────────────

export function ConnectionsArt() {
  const rows: [number, string, string, string][] = [
    [2, 'DIRECT', 'same resource', 'Pod shop/api-7-b2: container memory limit reached'],
    [6, 'OWNERSHIP', 'its owner', 'Deployment shop/api: spec written by kubectl-edit'],
    [9, 'REFERENCE', 'config it references', 'ConfigMap shop/api-config: keys or values written'],
    [31, 'SAME_RESOURCE_CONTEXT', 'the Node it ran on', 'Node worker-2: MemoryPressure became True'],
  ]
  return (
    <div className="conn">
      <div className="conn-head">
        <span className="conn-mark" aria-hidden="true"><svg viewBox="0 0 12 12" width="16" height="16"><path d="M6 .8L11.2 6 6 11.2.8 6z" fill="var(--fail)" /><path d="M4.4 4.4l3.2 3.2M7.6 4.4L4.4 7.6" stroke="var(--bg)" strokeWidth="1.3" /></svg></span>
        <span><span className="mono">10:42</span> Pod shop/api-7-b2 restarted (OOMKilled)</span>
      </div>
      <span className="label conn-title">{t('Recorded before this')}</span>
      <ol className="conn-list">
        {rows.map(([ago, cls, how, what]) => (
          <li key={cls}>
            <span className="mono conn-ago">{t('{n} min before', { n: ago })}</span>
            <span className={`chip class-${cls.toLowerCase()}`}>{t(how)}</span>
            <span className="conn-what">{what}</span>
          </li>
        ))}
      </ol>
    </div>
  )
}

// ── step 6: the Overview ──────────────────────────────

function sampleRange(): { lanes: TimelineLane[]; from: string; to: string; now: string; gaps: Range['gaps']; maint?: { from: string; to: string; name: string }; exceptions: { when: string; title: string; red: boolean }[] } {
  const maint = { from: at(7), to: at(9), name: 'Platform upgrade' }
  return {
    from: at(0), to: at(24), now: at(13), gaps: [],
    lanes: [
      { key: 'a', name: 'Nightly backup', meta: 'data', marks: [{ start: at(3, 15), end: at(3, 50), state: 'failed' }] },
      { key: 'b', name: 'Reconcile ledger', meta: 'payments', marks: [...exMarks([0, 2, 4, 6, 8, 10, 12], 'succeeded', 6), ...exMarks([14, 16, 18, 20, 22], 'expected', 6)], maintenance: [{ ...maint, namespaces: [], recurring: true }] },
      { key: 'c', name: 'Sales report', meta: 'data', marks: [...exMarks([0, 6, 12], 'succeeded', 10), ...exMarks([18], 'expected', 10)] },
    ],
    maint,
    exceptions: [{ when: '03:15', title: t('{name} failed', { name: 'Nightly backup' }), red: true }],
  }
}

export function OverviewArt({ tz }: { tz: string }) {
  const { data } = useApi<Range>(`/api/v1/range${rangeQuery({ kind: 'today' }, tz)}`)
  const live = !!data && data.lanes.length > 0
  const s = live ? null : sampleRange()
  const from = live ? data.from : s!.from
  const to = live ? data.to : s!.to
  const now = live ? data.now : s!.now
  const lanes: TimelineLane[] = live
    ? data.lanes.slice(0, 3).map((l) => ({
      key: l.operationId, name: l.name, meta: l.namespace, marks: l.marks,
      buckets: l.mode === 'hourly' ? l.buckets ?? [] : undefined, bucketMs: data.bucketSeconds * 1000,
      maintenance: (data.maintenance ?? []).filter((w) => w.namespaces.length === 0 || w.namespaces.includes(l.namespace)),
    }))
    : s!.lanes
  const ex = live
    ? data.exceptions.slice(0, 2).map((e) => { const x = exceptionText(e, tz, data.from, data.to); return { when: x.when, title: x.title, red: e.kind === 'failing' || e.kind === 'failed' || e.kind === 'missed' } })
    : s!.exceptions
  const tzShown = live ? tz : 'UTC'
  const maint = live ? (data.maintenance ?? [])[0] : s!.maint
  return (
    <Figure live={live}>
      <div className="mini">
        <Block n={1}>
          <span className="seg mini-seg">
            {['Today', '24 h', '5 d', 'Custom…'].map((p, i) => <span key={p} className={i === 0 ? 'on' : undefined}>{t(p)}</span>)}
          </span>
        </Block>
        <Block n={2}>{live ? <div className="mini-realstrip"><Strip tz={tz} span="5d" setSpan={() => {}} win={{ from: Date.parse(data.from), to: Date.parse(data.to) }} nowMs={Date.parse(data.now)} onChange={() => {}} /></div> : <MiniStrip />}</Block>
        <Block n={3}>
          <div className="ob-tl">
            <Timeline label={t('Operation')} from={from} to={to} tz={tzShown} now={now} gaps={live ? data.gaps : s!.gaps} lanes={lanes} compact labelWidth={128} watchedSince={live ? data.watchedSince : undefined} />
          </div>
        </Block>
        <Block n={4}><Legend items={['succeeded', 'failed', 'expected', 'gap']} maintenance /></Block>
        <Block n={5}>
          <div className="mini-rows">
            <span className="label">{t('Exceptions')}</span>
            {ex.length === 0 ? <span className="muted">{t('Nothing needs a look.')}</span> : ex.map((e, i) => (
              <span key={i} className="mini-row"><span className={`mono${e.red ? ' fail' : ' muted'}`}>{e.when}</span><span>{e.title}</span></span>
            ))}
          </div>
        </Block>
        <Block n={6}>
          {maint ? (
            <span className="mini-maint"><span className="chip maint" style={{ marginLeft: 0 }}>{t('maintenance: {name}', { name: maint.name })}</span><span className="mono">{hhmm(maint.from, tzShown)}–{hhmm(maint.to, tzShown)}</span></span>
          ) : <span className="muted">{t('No maintenance window declared.')}</span>}
        </Block>
      </div>
    </Figure>
  )
}

/** Five days of runs as small bars, the chosen window outlined: the Overview's strip, drawn small. */
function MiniStrip() {
  const bars = [3, 2, 4, 2, 3, 5, 2, 3, 2, 4, 3, 2, 3, 2, 5, 3, 2, 4, 2, 3, 3, 2, 4, 3, 2, 3, 5, 2, 3, 4]
  return (
    <svg className="mini-strip" viewBox="0 0 300 40" preserveAspectRatio="none" aria-hidden="true">
      <rect x="0" y="0" width="300" height="40" fill="var(--sel)" />
      {[60, 120, 180, 240].map((x) => <line key={x} x1={x} x2={x} y1="0" y2="40" stroke="var(--rule)" />)}
      {bars.map((h, i) => <rect key={i} x={i * 10 + 2} y={40 - h * 6} width="5" height={h * 6} fill={i === 26 ? 'var(--fail)' : 'var(--run)'} />)}
      <rect x="160" y="18" width="40" height="22" fill="url(#mh)" opacity=".7" />
      <defs><pattern id="mh" width="5" height="5" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><line x1="0" y1="0" x2="0" y2="5" stroke="var(--hatch)" strokeWidth="1.5" /></pattern></defs>
      <rect x="241" y="1" width="58" height="38" fill="none" stroke="var(--ink)" strokeWidth="2" />
    </svg>
  )
}

// ── step 7: the Explorer ──────────────────────────────

const failureLike = (r: ExplorerRecord) => r.severity === 'error' || r.severity === 'warning'

export function ExplorerArt({ tz }: { tz: string }) {
  const { data } = useApi<ExplorerList>('/api/v1/explorer?range=24h&limit=50')
  const pick = data?.records.find(failureLike)
  const { data: before } = useApi<ExplorerBefore>(pick ? `/api/v1/explorer/records/${encodeURIComponent(pick.id)}/before?window=1h` : null)
  const live = !!data && data.records.length > 0 && !!pick
  const rows: { at: string; summary: string; prov: string; sel: boolean; sev?: string }[] = live
    ? [pick!, ...data!.records.filter((r) => r.id !== pick!.id).slice(0, 2)].sort((a, b) => b.at.localeCompare(a.at)).map((r) => ({ at: hhmm(r.at, tz), summary: r.summary, prov: r.provenance, sel: r.id === pick!.id, sev: r.severity }))
    : [
      { at: '10:42', summary: 'Pod shop/api-7-b2 restarted (OOMKilled)', prov: 'OBSERVED', sel: true, sev: 'warning' },
      { at: '10:36', summary: 'Deployment shop/api: spec written by kubectl-edit', prov: 'OBSERVED', sel: false },
      { at: '10:00', summary: 'Reconcile ledger succeeded', prov: 'DERIVED', sel: false },
    ]
  const changes: { ago: string; cls: string; how: string; what: string }[] = live && before
    ? before.changes.slice(0, 3).map((c: BeforeChange) => ({ ago: t('{n} min before', { n: Math.max(1, Math.round(c.secondsBefore / 60)) }), cls: c.classification, how: howRelated(c), what: c.record.summary }))
    : [
      { ago: t('{n} min before', { n: 6 }), cls: 'OWNERSHIP', how: t('its owner'), what: 'Deployment shop/api: spec written by kubectl-edit' },
      { ago: t('{n} min before', { n: 9 }), cls: 'REFERENCE', how: t('config it references'), what: 'ConfigMap shop/api-config: keys or values written' },
    ]
  const sel = rows.find((r) => r.sel)!
  return (
    <Figure live={live}>
      <div className="mini">
        <div className="mini-top">
          <Block n={1}>
            <span className="mini-filters">
              <span className="chip">24 h</span><span className="chip">{t('kind')}: {t('any')}</span><span className="chip">namespace: shop</span>
              <span className="mini-search">{t('Search records')}</span>
            </span>
          </Block>
          <span className="mini-actions">
            <Pin n={5} /><span className="mini-btn">{t('Workspace')} · 2</span>
            <Pin n={6} /><span className="mini-btn">{t('Export JSON')}</span>
          </span>
        </div>
        <div className="mini-split">
          <Block n={2}>
            <div className="mini-records">
              {rows.map((r, i) => (
                <div key={i} className={`mini-rec${r.sel ? ' sel' : ''}${r.sev === 'warning' || r.sev === 'error' ? ' warn' : ''}`}>
                  <span className="mono">{r.at}</span>
                  <span className="mini-sum">{r.summary}</span>
                  <span className={`chip prov-${r.prov.toLowerCase()}`}>{provWord(r.prov)}</span>
                  <span className="ws-toggle" aria-hidden="true">{i === 0 ? '✓' : '+'}</span>
                </div>
              ))}
            </div>
          </Block>
          <div className="mini-panel">
            <Block n={3}>
              <span className="mini-panel-name">{sel.summary}</span>
              <span className="muted"><span className="mono">{sel.at}</span> · <span className={`chip prov-${sel.prov.toLowerCase()}`} style={{ marginLeft: 0 }}>{provWord(sel.prov)}</span></span>
            </Block>
            <Block n={4}>
              <span className="label">{t('Recorded before this')}</span>
              {changes.length === 0 ? <span className="muted">{t('No changes recorded in this window.')}</span> : changes.map((c, i) => (
                <span key={i} className="mini-before">
                  <span><span className="mono muted">{c.ago}</span> <span className={`chip class-${c.cls.toLowerCase()}`} style={{ marginLeft: 0 }}>{c.how}</span></span>
                  <span className="mini-sum">{c.what}</span>
                </span>
              ))}
            </Block>
          </div>
        </div>
      </div>
    </Figure>
  )
}

function howRelated(c: { classification: string; subject: { kind: string } }): string {
  switch (c.classification) {
    case 'DIRECT': return t('same resource')
    case 'OWNERSHIP': return t('its owner')
    case 'REFERENCE': return t('config it references')
    case 'SAME_RESOURCE_CONTEXT': return c.subject.kind === 'Node' ? t('the Node it ran on') : t('same resource context')
  }
  return t('unresolved')
}

// ── step 8: any moment ────────────────────────────────

/** A short weekday name in the reader's language: 0 is a Monday. */
const weekday = (i: number) => new Date(Date.UTC(2026, 2, 2 + i, 12)).toLocaleDateString(locale(), { weekday: 'short', timeZone: 'UTC' })

export function PointInTimeArt() {
  const rows: [string, string, string][] = [
    ['Deployment shop/api', 'shop/api:2.3.0', 'shop/api:2.4.0'],
    ['StatefulSet data/postgres', 'postgres:16.4', 'postgres:16.4'],
    ['DaemonSet kube-system/node-agent', 'agent:1.9.2', 'agent:1.10.0'],
  ]
  return (
    <div className="pit">
      <div className="pit-labels pit-at mono"><span className="at" style={{ left: '42%' }}>{weekday(1)} 14:00</span><span className="nowl" style={{ right: 0 }}>{t('now')}</span></div>
      <div className="pit-track" aria-hidden="true">
        {[0, 25, 50, 75].map((x) => <i key={x} className="tick" style={{ left: `${x}%` }} />)}
        <i className="now" style={{ left: '100%' }} />
        <span className="handle" style={{ left: '42%' }}><i /></span>
      </div>
      <div className="pit-labels mono">
        {[0, 1, 2, 3].map(weekday).map((d, i) => <span key={d} style={{ left: `${i * 25}%` }}>{d}</span>)}
      </div>
      <table className="data pit-table">
        <thead><tr><th className="label">{t('Workload')}</th><th className="label">{weekday(1)} 14:00</th><th className="label">{t('now')}</th></tr></thead>
        <tbody>
          {rows.map(([w, a, b]) => (
            <tr key={w}><td>{w}</td><td className="mono">{a}</td><td className="mono">{a === b ? <span className="muted">{b}</span> : <b>{b}</b>}</td></tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ── step 9: schedules ─────────────────────────────────

const cellWords: Record<CellState, string> = { none: 'nothing due', suspended: 'suspended', ok: 'every observed run succeeded', unconfirmed: 'not confirmed', missed: 'did not start', failed: 'a run failed' }

function sampleOps(): OperationRow[] {
  const period = (p50: number, runs: number) => ({ from: '', to: '', runs, p50Seconds: p50, p95Seconds: p50 * 1.3, whole: true, notWatchedSeconds: 0 })
  const op = (name: string, ns: string, week: CellState[], p50: number, base: number, longer: boolean) => ({
    id: name, name, displayName: name, namespace: ns, schedule: '', concurrency: '', successfulJobsHistoryLimit: 3, failedJobsHistoryLimit: 1, createdAt: '', firstSeenAt: '',
    week: week.map((s, i) => ({ date: `2026-03-0${i + 1}`, state: s, runs: 1 })),
    duration: { provenance: 'DERIVED', recent: period(p50, 7), baseline: period(base, 23), minRuns: 5, enough: true, ratio: p50 / base, longerAt: 1.3, longer },
  }) as unknown as OperationRow
  return [
    op('Nightly backup', 'data', ['ok', 'ok', 'ok', 'ok', 'ok', 'ok', 'failed'], 1450, 1010, true),
    op('Reconcile ledger', 'payments', ['ok', 'ok', 'ok', 'ok', 'ok', 'unconfirmed', 'ok'], 62, 60, false),
    op('Sales report', 'data', ['ok', 'ok', 'missed', 'ok', 'ok', 'ok', 'ok'], 250, 245, false),
  ]
}

export function SchedulesArt({ tz }: { tz: string }) {
  const { data } = useApi<Operations>(`/api/v1/operations${query({ tz })}`)
  const live = !!data && data.operations.length > 0
  const ops = live ? [...data.operations].sort((a, b) => Number(b.duration.longer) - Number(a.duration.longer)).slice(0, 3) : sampleOps()
  return (
    <Figure live={live}>
      <table className="data mini-table">
        <thead><tr><th className="label">{t('Operation')}</th><th className="label">{t('Last 7 days')}</th><th className="label">{t('Run time')}</th></tr></thead>
        <tbody>
          {ops.map((o) => (
            <tr key={o.id}>
              <td><span style={{ fontWeight: 500 }}>{o.displayName || o.name}</span><span className="mono muted" style={{ display: 'block', fontSize: 13 }}>{o.namespace}</span></td>
              <td>
                <span className="week">
                  {o.week.map((c) => <i key={c.date} className={c.state} title={`${shortDay(`${c.date}T12:00:00Z`, 'UTC')}: ${t(cellWords[c.state])}`} />)}
                </span>
              </td>
              <td><DurationCell d={o.duration} /></td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="legend" style={{ paddingTop: 4 }}>
        <span><span className="week"><i className="ok" /></span>{t('succeeded')}</span>
        <span><span className="week"><i className="failed" /></span>{t('a run failed')}</span>
        <span><span className="week"><i className="missed" /></span>{t('did not start')}</span>
        <span><span className="week"><i className="unconfirmed" /></span>{t('not confirmed')}</span>
        <span><LongerMark />{t('longer than its baseline')}</span>
      </div>
    </Figure>
  )
}

// ── step 10: System ───────────────────────────────────

export function SystemArt({ sys }: { sys?: System }) {
  const live = !!sys && sys.scopes.length > 0
  const from = live ? Date.parse(sys.from) : 0
  const to = live ? Math.max(from + 3600e3, Date.parse(sys.now)) : from + 24 * 3600e3
  const lanes = live
    ? [...new Map(sys.scopes.map((s) => [s.kind, s])).values()].slice(0, 6).map((s) => ({ name: t(kindName[s.kind] ?? s.kind), on: s.watching.map((w) => [Date.parse(w.from), Date.parse(w.to)]), problem: !!s.problem }))
    : [
      { name: t('CronJobs'), on: [[0, 0.58], [0.7, 1]], problem: false },
      { name: t('Jobs'), on: [[0, 0.58], [0.7, 1]], problem: false },
      { name: t('Pods'), on: [[0, 1]], problem: false },
      { name: t('Nodes'), on: [], problem: true },
    ].map((l) => ({ ...l, on: l.on.map(([a, b]) => [from + a * (to - from), from + b * (to - from)]) }))
  const pct = (x: number) => `${Math.max(0, Math.min(100, ((x - from) / (to - from)) * 100))}%`
  const nowMs = to
  return (
    <Figure live={live}>
      <div className="sys-lanes">
        {lanes.map((l) => (
          <div key={l.name} className="sys-lane">
            <span className="sys-name">{l.name}</span>
            <span className="cov-track">
              {l.on.map(([a, b], i) => <i key={i} className="on" style={{ left: pct(a), width: `calc(${pct(Math.min(b, nowMs))} - ${pct(a)})` }} />)}
              {gapsOf(l.on, from, nowMs).map(([a, b], i) => <i key={`g${i}`} className="gap" style={{ left: pct(a), width: `calc(${pct(b)} - ${pct(a)})` }} />)}
            </span>
            <span className={`mono sys-state${l.problem ? ' fail' : ''}`}>{l.problem ? t('not allowed') : gapsOf(l.on, from, nowMs).length ? t('has gaps') : t('watching')}</span>
          </div>
        ))}
      </div>
      <div className="cov-legend">
        <span><i className="sw on" />{t('watching')}</span>
        <span><i className="sw gap" />{t('not watching')}</span>
      </div>
      <dl className="sys-facts">
        <div><dt className="label">{t('Kept')}</dt><dd className="mono">{t('{n} days', { n: live ? sys.storage.retentionDays : 30 })}</dd></div>
        <div><dt className="label">{t('At most')}</dt><dd className="mono">{(live ? sys.storage.maxEntries : 2_000_000).toLocaleString(locale())}</dd></div>
        <div><dt className="label">{t('Records')}</dt><dd className="mono">{(live ? sys.storage.entries : 184_302).toLocaleString(locale())}</dd></div>
      </dl>
    </Figure>
  )
}

function gapsOf(on: number[][], from: number, until: number): number[][] {
  if (on.length === 0) return [[from, until]]
  const out: number[][] = []
  let cur = from
  for (const [a, b] of [...on].sort((x, y) => x[0] - y[0])) {
    if (a > cur + 60_000) out.push([cur, Math.min(a, until)])
    cur = Math.max(cur, b)
  }
  if (cur < until - 60_000) out.push([cur, until])
  return out.filter(([a, b]) => b > a)
}

// ── step 11: integrations ─────────────────────────────

export function IntegrationsArt({ sys }: { sys?: System }) {
  const on = (v: boolean | undefined, yes: string, no: string) => (v === undefined ? null : <span className={`chip${v ? '' : ' off'}`} style={{ marginLeft: 0 }}>{t(v ? yes : no)}</span>)
  const items: { icon: ReactNode; name: string; what: string; where: string[]; state: ReactNode }[] = [
    { icon: <MetricsIcon />, name: 'Prometheus metrics', what: "OpsTide's own health and each CronJob's last success.", where: ['/metrics', 'serviceMonitor.enabled'], state: null },
    { icon: <AnnotIcon />, name: 'Grafana annotations', what: 'Changes, failures and gaps on your dashboards.', where: ['/api/v1/annotations', 'publicURL'], state: null },
    { icon: <MaintIcon />, name: 'Maintenance windows', what: 'Planned work reads as planned, and is still shown.', where: ['maintenance.windows'], state: sys ? <span className="chip" style={{ marginLeft: 0 }}>{t('{n} declared', { n: sys.maintenance?.windows.length ?? 0 })}</span> : null },
    { icon: <KeyIcon />, name: 'ConfigMaps and Secrets', what: 'Who wrote them, never a value. Secret metadata is opt-in.', where: ['watch.configMaps', 'watch.secretMetadata'], state: sys ? <>{on(sys.config.configMaps, 'ConfigMaps on', 'ConfigMaps off')} {on(sys.config.secretMetadata, 'Secrets on', 'Secrets off')}</> : null },
  ]
  return (
    <Figure live={!!sys}>
      <ul className="integ">
        {items.map((x) => (
          <li key={x.name}>
            <span className="integ-icon" aria-hidden="true">{x.icon}</span>
            <span className="integ-text"><b>{t(x.name)}</b><span>{t(x.what)}</span></span>
            <span className="integ-where">{x.where.map((w) => <code key={w} className="mono">{w}</code>)}{x.state && <span className="integ-state">{x.state}</span>}</span>
          </li>
        ))}
      </ul>
    </Figure>
  )
}

const I = ({ children }: { children: ReactNode }) => <svg width="28" height="28" viewBox="0 0 28 28" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round">{children}</svg>
const MetricsIcon = () => <I><path d="M4 23h20" /><path d="M6 19l5-6 4 3 7-9" /><circle cx="22" cy="7" r="1.6" fill="currentColor" /></I>
const AnnotIcon = () => <I><path d="M4 23h20" /><path d="M4 18c4-6 7 2 11-4s6-4 9-6" /><path d="M15 4v19" strokeDasharray="2 2.5" /><path d="M15 4h5l-1.5 2 1.5 2h-5" /></I>
const MaintIcon = () => <I><rect x="4" y="6" width="20" height="17" /><path d="M4 11h20M9 3v5M19 3v5" /><path d="M8 15h12v4H8z" fill="currentColor" opacity=".25" /></I>
const KeyIcon = () => <I><circle cx="10" cy="14" r="5" /><path d="M15 14h10M21 14v4M24 14v3" /></I>

// ── step 12: watching now ─────────────────────────────

export function WatchingArt({ sys, failed }: { sys?: System; failed: boolean }) {
  if (failed) return <div className="error" role="alert">{t('Could not reach the collector. The rest of this guide still applies.')}</div>
  if (!sys) return <div className="empty" aria-busy="true" />
  const kinds = [...new Map(sys.scopes.map((s) => [s.kind, sys.scopes.filter((x) => x.kind === s.kind)])).entries()]
  if (kinds.length === 0) return <div className="empty">{t('No watch has started yet.')}</div>
  return (
    <ul className="watch-grid">
      {kinds.map(([kind, scopes]) => {
        const problem = scopes.find((s) => s.problem)?.problem
        return (
          <li key={kind} className={problem ? 'bad' : undefined} title={problem}>
            {problem
              ? <svg viewBox="0 0 16 16" width="18" height="18" aria-hidden="true"><path d="M8 1l7 7-7 7-7-7z" fill="var(--fail)" /><path d="M5.8 5.8l4.4 4.4M10.2 5.8l-4.4 4.4" stroke="var(--bg)" strokeWidth="1.6" /></svg>
              : <svg viewBox="0 0 16 16" width="18" height="18" aria-hidden="true"><circle cx="8" cy="8" r="7" fill="var(--run)" /><path d="M4.6 8.3l2.2 2.2 4.6-4.8" fill="none" stroke="var(--bg)" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" /></svg>}
            <span className="watch-name">{t(kindName[kind] ?? kind)}</span>
            <span className={`watch-state mono${problem ? ' fail' : ''}`}>{problem ? (/forbidden/i.test(problem) ? t('not allowed') : t('failing')) : t('watching')}</span>
          </li>
        )
      })}
    </ul>
  )
}
