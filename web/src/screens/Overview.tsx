import { useState } from 'react'
import { type Entries, type Entry, type Exception, type Range } from '../api'
import { Strip } from '../components/Strip'
import { Failed, Loading } from '../components/Shell'
import { Legend, Timeline, type TimelineLane } from '../components/Timeline'
import { useApi } from '../live'
import { exceptionText, rangeSentence } from '../narrative'
import { entryWord } from '../entryWords'
import { Link, navigate, withParams } from '../router'
import { hhmm, offset, utcToZoned, zonedToUtc } from '../time'
import { HOUR, MAX_WINDOW, MIN_WINDOW, MINUTE, params as selParams, presets, presetSelection, pressedPreset, rangeQuery, selection, selectionFor, title, type Selection } from '../window'
import { ZoneSelect } from './common'

export function Overview({ params, tz, setTz }: { params: URLSearchParams; tz: string; setTz: (z: string) => void }) {
  const sel = selection(params)
  const span = params.get('span') ?? '5d'
  const [customOpen, setCustomOpen] = useState(false)
  // Windows that follow now are refetched every 30 s; the previous answer stays on screen while a new window loads.
  const { data, error, loading } = useApi<Range>(`/api/v1/range${rangeQuery(sel, tz)}`, { keep: true, refreshMs: sel.kind === 'today' || sel.kind === 'range' ? 30_000 : undefined })
  if (error && !data) return <div className="page"><Failed error={error} /></div>
  if (!data) return <div className="page"><Loading /></div>

  const nowMs = Date.parse(data.now)
  const go = (s: Selection) => navigate(withParams(params, selParams(s)), true)
  const t = title(sel, data.from, data.to, tz, data.now)
  const pressed = pressedPreset(sel, data.now, tz)
  const live = Date.parse(data.to) >= nowMs - 2 * MINUTE
  const hasNow = nowMs >= Date.parse(data.from) && nowMs <= Date.parse(data.to)
  const win = { from: Date.parse(data.from), to: Date.parse(data.to) }

  const lanes: TimelineLane[] = data.lanes.map((l) => ({
    key: l.operationId,
    name: l.name,
    meta: `${l.namespace} · ${l.schedule}${l.suspended ? ' · suspended' : ''}`,
    href: `/operations/${encodeURIComponent(l.operationId)}`,
    marks: l.marks,
    buckets: l.mode === 'hourly' ? l.buckets ?? [] : undefined,
    bucketMs: data.bucketSeconds * 1000,
  }))
  if (data.rollouts.length) {
    lanes.push({
      key: 'rollouts', name: 'Rollouts', meta: 'Deployments, observed', href: '/log?show=rollouts',
      marks: data.rollouts.map((r) => ({ start: r.at, state: 'running' as const, why: `${hhmm(r.at, tz)} ${r.summary}` })),
      markTitle: (m) => m.why ?? '',
    })
  }
  const anyHourly = data.lanes.some((l) => l.mode === 'hourly')

  return (
    <div className="page" aria-busy={loading} style={{ opacity: loading ? 0.7 : 1, transition: 'opacity .15s' }}>
      <div className="head-row">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">Window</span>
          <h1 className="title">{t.title}</h1>
          <span className="mono muted" style={{ fontSize: 13 }}>{t.sub} · {offset(tz, new Date(data.from))}</span>
        </div>
        <span className="grow" />
        <div className="seg" role="group" aria-label="Time range">
          {presets.map((p) => (
            <button key={p.key} type="button" aria-pressed={pressed === p.key} onClick={() => go(presetSelection(p.key, data.now, tz))}>{p.label}</button>
          ))}
          <button type="button" aria-pressed={sel.kind === 'custom' || customOpen} onClick={() => setCustomOpen((o) => !o)}>Custom…</button>
        </div>
        {sel.kind === 'custom' && !live && (
          <button type="button" className="button" style={{ height: 32 }} title="Same length, ending now"
            onClick={() => go(selectionFor({ from: nowMs - (win.to - win.from), to: nowMs }, nowMs))}>Follow now</button>
        )}
        <ZoneSelect tz={tz} setTz={setTz} compact />
      </div>

      {customOpen && <CustomRange key={`${data.from}${data.to}${tz}`} tz={tz} from={win.from} to={win.to} nowMs={nowMs} go={go} />}

      <Strip tz={tz} span={span} setSpan={(s) => navigate(withParams(params, { span: s === '5d' ? null : s }), true)} win={win} nowMs={nowMs} onChange={go} />

      <p className="sentence">{rangeSentence(data, tz, sel.kind === 'today')}</p>

      <section aria-label="Window chart" style={{ display: 'flex', flexDirection: 'column' }}>
        {lanes.length === 0 ? (
          <div className="empty">
            {data.idle > 0 ? `Nothing ran or was due in this window. ${data.idle} CronJob${data.idle === 1 ? ' is' : 's are'} idle in it or suspended.` : 'No CronJobs seen yet. Once the collector sees CronJobs, they appear here.'}
          </div>
        ) : (
          <>
            {anyHourly && (
              <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 14px', paddingBottom: 10 }}>
                <span className="mono" style={{ fontSize: 12, padding: '2px 8px', border: '1px solid var(--line)' }}>runs per hour</span>
                <span className="muted" style={{ fontSize: 12 }}>A lane with too many runs to draw one by one shows how many there were per hour, failures in red. Narrow the window to see single runs.</span>
              </div>
            )}
            <Timeline label="Operation" from={data.from} to={data.to} tz={tz} now={hasNow ? data.now : undefined} gaps={data.gaps}
              watchedSince={data.watchedSince} lanes={lanes}
              markHref={(m, lane) => (lane.key === 'rollouts' ? undefined : `/operations/${encodeURIComponent(lane.key)}${m.runId ? `?run=${encodeURIComponent(m.runId)}` : ''}`)} />
            <Legend before={!!data.watchedSince && Date.parse(data.watchedSince) > Date.parse(data.from)} note={`Bar width is the run's duration.${data.idle ? ` ${data.idle} CronJob${data.idle === 1 ? '' : 's'} had nothing in this window.` : ''}`} />
          </>
        )}
      </section>

      <div className="two">
        <section aria-label="Exceptions" className="rows">
          <h2 className="label section-title">Exceptions <span className="note">· grouped, newest first</span></h2>
          {data.exceptions.length === 0 && <div className="empty">Nothing needs a look{data.gaps.length ? ', as far as OpsTide could see' : ''}.</div>}
          {data.exceptions.map((e, i) => <ExceptionRow key={i} e={e} tz={tz} from={data.from} to={data.to} nowMs={nowMs} go={go} />)}
        </section>

        <section aria-label="Coming up" className="rows">
          <h2 className="label section-title">Coming up <span className="note">· from the schedules</span></h2>
          {!hasNow && Date.parse(data.to) < nowMs && <div className="empty">Shown for windows that reach now.</div>}
          {(hasNow || Date.parse(data.to) > nowMs) && data.upcoming.length === 0 && <div className="empty">Nothing more is expected in this window.</div>}
          {data.upcoming.map((u) => (
            <Link key={u.operationId} className="row" href={`/operations/${encodeURIComponent(u.operationId)}`}>
              <span className="mono">{hhmm(u.at, tz)}</span>
              <span>{u.name}</span>
              <span className="mono muted" style={{ fontSize: 12 }}>{u.namespace}</span>
            </Link>
          ))}
          <Link href="/schedules" style={{ paddingTop: 12, fontSize: 13 }}>All schedules</Link>
        </section>
      </div>
      <RecentChanges tz={tz} />
    </div>
  )
}

// What changed in the last day, in order: revisions, reported availability, Node conditions and the Warning
// Events Kubernetes recorded. It is a list of what was reported, not a verdict: scheduled operations that failed
// are under Exceptions, and an empty list means nothing of these kinds was recorded, not that nothing is wrong.
const changeKinds = [['revisions', 'Revisions'], ['availability', 'Availability'], ['nodes', 'Nodes'], ['evidence', 'Evidence']] as const
type ChangeKind = (typeof changeKinds)[number][0]

function changeKind(type: string): ChangeKind | null {
  if (type === 'release.observed' || type === 'release.stalled' || type === 'release.failed' || /^(statefulset|daemonset)\.(revision_changed|update_)/.test(type)) return 'revisions'
  if (type === 'release.unavailable' || type.endsWith('.counts_changed')) return 'availability'
  if (type.startsWith('node.')) return 'nodes'
  if (type.startsWith('evidence.')) return 'evidence'
  return null
}

function changeHref(e: Entry): string {
  if (e.kind === 'Node' && e.resourceUid) return `/nodes/${encodeURIComponent(e.resourceUid)}`
  if (e.releaseId && e.type.startsWith('release.')) return `/releases/${encodeURIComponent(e.releaseId)}`
  if (e.workloadId) return `/workloads/${encodeURIComponent(e.workloadId)}`
  return `/log?q=${encodeURIComponent(e.resourceName ?? '')}&range=7d`
}

function RecentChanges({ tz }: { tz: string }) {
  const { data } = useApi<Entries>('/api/v1/events?within=24h&show=releases,statefulsets,daemonsets,nodes,kubernetes,counts&limit=200', { refreshMs: 60_000 })
  const [off, setOff] = useState<Set<ChangeKind>>(new Set())
  const all = (data?.entries ?? []).filter((e) => changeKind(e.type))
  const shown = all.filter((e) => !off.has(changeKind(e.type)!)).slice(0, 14)
  const count = (k: ChangeKind) => all.filter((e) => changeKind(e.type) === k).length
  return (
    <section aria-label="Recent operational changes" className="rows">
      <h2 className="label section-title">Recent operational changes <span className="note">· last 24 hours, newest first</span></h2>
      <fieldset style={{ border: 0, padding: 0, margin: '0 0 8px' }}>
        <span className="checks">
          {changeKinds.map(([k, label]) => (
            <label key={k}><input type="checkbox" checked={!off.has(k)} onChange={() => setOff((o) => { const n = new Set(o); if (n.has(k)) n.delete(k); else n.add(k); return n })} />{label} ({count(k)})</label>
          ))}
        </span>
      </fieldset>
      {data && shown.length === 0 && <div className="empty">{data.nextCursor ? 'The newest entries of these kinds are all of another kind than the ones selected; the list is limited to 200 entries, open the Log for more.' : 'No revision, availability change, Node condition or Warning Event was recorded in the last 24 hours. That says what was recorded, not that nothing else happened.'}</div>}
      {data?.nextCursor && shown.length > 0 && <span className="muted" style={{ fontSize: 12 }}>Limited to the newest 200 entries of these kinds; the counts above are of those.</span>}
      {shown.map((e) => (
        <Link key={e.id} className="row" href={changeHref(e)} style={{ gridTemplateColumns: '60px 110px minmax(0, 1fr)' }}>
          <span className="mono">{hhmm(e.at, tz)}</span>
          <span className="label">{entryWord(e.type, { 'release.observed': 'Release', 'release.stalled': 'Stalled', 'release.failed': 'Failed', 'release.unavailable': 'Availability', 'statefulset.counts_changed': 'Counts', 'daemonset.counts_changed': 'Counts', 'evidence.event': 'K8s Event', 'evidence.count_changed': 'K8s Event' })}</span>
          <span style={{ overflowWrap: 'anywhere' }}>{e.summary}</span>
        </Link>
      ))}
      <Link href="/log?show=releases,statefulsets,daemonsets,nodes,kubernetes,counts&range=24h" style={{ paddingTop: 10, fontSize: 13 }}>All of these in the Log</Link>
    </section>
  )
}

function ExceptionRow({ e, tz, from, to, nowMs, go }: { e: Exception; tz: string; from: string; to: string; nowMs: number; go: (s: Selection) => void }) {
  const t = exceptionText(e, tz, from, to)
  const red = e.kind === 'failing' || e.kind === 'failed' || e.kind === 'missed'
  const href = e.kind === 'gap' ? '/system' : `/operations/${encodeURIComponent(e.operationId ?? '')}${e.runId ? `?run=${encodeURIComponent(e.runId)}` : ''}`
  const start = Date.parse(e.from ?? e.at)
  const end = Date.parse(e.to ?? e.at)
  const canShow = !!(e.from && e.to) // a stretch of time can be shown on the chart
  const show = () => {
    const pad = Math.max(15 * MINUTE, (end - start) / 4)
    const w = Math.min(Math.max(end - start + 2 * pad, MIN_WINDOW), MAX_WINDOW - HOUR)
    const a = start - (w - (end - start)) / 2
    go(selectionFor({ from: a, to: Math.min(a + w, nowMs) }, nowMs))
  }
  return (
    <div className="row" style={{ gridTemplateColumns: '150px minmax(0, 1fr) auto', alignItems: 'start' }}>
      <span className={`mono${red ? ' fail' : ' muted'}`} style={{ fontWeight: 500 }}>{t.when}</span>
      <Link href={href} style={{ display: 'flex', flexDirection: 'column', gap: 3, color: 'var(--ink)', textDecoration: 'none' }}>
        <span style={{ fontWeight: 500 }}>{t.title}</span>
        <span className="muted">{t.detail}</span>
      </Link>
      {canShow ? <button type="button" className="button" style={{ height: 28, padding: '0 10px', fontSize: 12 }} onClick={show}>Show</button> : <span />}
    </div>
  )
}

/** A day, or a from and to; "to" can follow now. Times are read in the chosen zone. */
function CustomRange({ tz, from, to, nowMs, go }: { tz: string; from: number; to: number; nowMs: number; go: (s: Selection) => void }) {
  const [f, setF] = useState(utcToZoned(from, tz))
  const [t, setT] = useState(utcToZoned(to, tz))
  const [follow, setFollow] = useState(Math.abs(to - nowMs) <= 2 * MINUTE)
  const [day, setDay] = useState('')
  const a = zonedToUtc(f, tz)
  const b = follow ? nowMs : zonedToUtc(t, tz)
  let problem = ''
  if (isNaN(a) || isNaN(b)) problem = 'Enter both times.'
  else if (b - a < MIN_WINDOW) problem = 'The window must be at least 5 minutes, and end after it starts.'
  else if (b - a > MAX_WINDOW) problem = 'The window can be at most 7 days. The strip above covers longer spans.'
  return (
    <div className="customrow" role="group" aria-label="Custom range">
      <span className="label">Custom</span>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}><span className="label">From</span>
        <input className="control mono" type="datetime-local" value={f} onChange={(e) => setF(e.target.value)} />
      </label>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}><span className="label">To</span>
        <input className="control mono" type="datetime-local" value={follow ? utcToZoned(nowMs, tz) : t} disabled={follow} onChange={(e) => setT(e.target.value)} />
      </label>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--muted)' }}>
        <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />Keep the end at now
      </label>
      <button type="button" className="button" disabled={!!problem} style={{ background: problem ? undefined : 'var(--ink)', color: problem ? undefined : 'var(--bg)', fontWeight: 600 }}
        onClick={() => go(selectionFor({ from: a, to: b }, nowMs))}>Apply</button>
      <span style={{ flex: 1 }} />
      <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}><span className="label">Or a day</span>
        <input className="control mono" type="date" value={day} onChange={(e) => { setDay(e.target.value); if (e.target.value) go({ kind: 'date', date: e.target.value }) }} />
      </label>
      <span className="muted" style={{ fontSize: 12, flexBasis: '100%' }}>{problem || 'Up to 7 days. The link keeps the window, so it can be shared.'}</span>
    </div>
  )
}
