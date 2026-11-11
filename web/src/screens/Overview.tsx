import { useState } from 'react'
import { explorerLink, type ExplorerSummary } from '../explorer'
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
import { Note } from '../components/Note'
import { t, tn } from '../i18n'

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
  const head = title(sel, data.from, data.to, tz, data.now)
  const pressed = pressedPreset(sel, data.now, tz)
  const live = Date.parse(data.to) >= nowMs - 2 * MINUTE
  const hasNow = nowMs >= Date.parse(data.from) && nowMs <= Date.parse(data.to)
  const win = { from: Date.parse(data.from), to: Date.parse(data.to) }

  const lanes: TimelineLane[] = data.lanes.map((l) => ({
    key: l.operationId,
    name: l.name,
    meta: `${l.namespace} · ${l.schedule}${l.suspended ? ` · ${t('suspended')}` : ''}`,
    href: `/operations/${encodeURIComponent(l.operationId)}`,
    marks: l.marks,
    buckets: l.mode === 'hourly' ? l.buckets ?? [] : undefined,
    bucketMs: data.bucketSeconds * 1000,
    maintenance: (data.maintenance ?? []).filter((w) => w.namespaces.length === 0 || w.namespaces.includes(l.namespace)),
  }))
  if (data.rollouts.length) {
    lanes.push({
      key: 'rollouts', name: t('Rollouts'), meta: t('Deployments, observed'), href: '/explorer?range=24h&group=releases',
      marks: data.rollouts.map((r) => ({ start: r.at, state: 'running' as const, why: `${hhmm(r.at, tz)} ${r.summary}` })),
      markTitle: (m) => m.why ?? '',
    })
  }
  const anyHourly = data.lanes.some((l) => l.mode === 'hourly')

  return (
    <div className="page" aria-busy={loading} style={{ opacity: loading ? 0.7 : 1, transition: 'opacity .15s' }}>
      <LastDay />
      <div className="head-row">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">{t('Scheduled operations · window')}</span>
          <h2 className="title" style={{ fontSize: 28 }}>{head.title}</h2>
          <span className="mono muted" style={{ fontSize: 13 }}>{head.sub} · {offset(tz, new Date(data.from))}</span>
        </div>
        <span className="grow" />
        <div className="seg" role="group" aria-label={t('Time range')}>
          {presets.map((p) => (
            <button key={p.key} type="button" aria-pressed={pressed === p.key} onClick={() => go(presetSelection(p.key, data.now, tz))}>{p.label}</button>
          ))}
          <button type="button" aria-pressed={sel.kind === 'custom' || customOpen} onClick={() => setCustomOpen((o) => !o)}>{t('Custom…')}</button>
        </div>
        {sel.kind === 'custom' && !live && (
          <button type="button" className="button" style={{ height: 32 }} title={t('Same length, ending now')}
            onClick={() => go(selectionFor({ from: nowMs - (win.to - win.from), to: nowMs }, nowMs))}>{t('Follow now')}</button>
        )}
        <ZoneSelect tz={tz} setTz={setTz} compact />
      </div>

      {customOpen && <CustomRange key={`${data.from}${data.to}${tz}`} tz={tz} from={win.from} to={win.to} nowMs={nowMs} go={go} />}

      <Strip tz={tz} span={span} setSpan={(s) => navigate(withParams(params, { span: s === '5d' ? null : s }), true)} win={win} nowMs={nowMs} onChange={go} />

      <p className="sentence">{rangeSentence(data, tz, sel.kind === 'today')}</p>

      <section aria-label={t('Window chart')} style={{ display: 'flex', flexDirection: 'column' }}>
        {lanes.length === 0 ? (
          <div className="empty">
            {data.idle > 0 ? tn(data.idle, 'Nothing ran or was due in this window. {n} CronJob is idle in it or suspended.', 'Nothing ran or was due in this window. {n} CronJobs are idle in it or suspended.') : t('No CronJobs seen yet. Once the collector sees CronJobs, they appear here.')}
          </div>
        ) : (
          <>
            {anyHourly && (
              <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px 14px', paddingBottom: 10 }}>
                <span className="mono" style={{ fontSize: 12, padding: '2px 8px', border: '1px solid var(--line)' }}>{t('runs per hour')}</span>
                <Note more={t('A lane with too many runs to draw one by one shows how many there were per hour, failures in red. Narrow the window to see single runs.')}>{t('drawn per hour')}</Note>
              </div>
            )}
            <Timeline label={t('Operation')} from={data.from} to={data.to} tz={tz} now={hasNow ? data.now : undefined} gaps={data.gaps}
              watchedSince={data.watchedSince} lanes={lanes}
              markHref={(m, lane) => (lane.key === 'rollouts' ? undefined : `/operations/${encodeURIComponent(lane.key)}${m.runId ? `?run=${encodeURIComponent(m.runId)}` : ''}`)} />
            {data.maintenanceTruncated && <span className="muted" style={{ fontSize: 12 }}>{t('More maintenance window occurrences fall in this window than are drawn; the band shows only the first. Records are still marked. Narrow the window to see every occurrence.')}</span>}
            <Legend maintenance={(data.maintenance ?? []).length > 0} before={!!data.watchedSince && Date.parse(data.watchedSince) > Date.parse(data.from)} note={`${t("Bar width is the run's duration.")}${data.idle ? ` ${tn(data.idle, '{n} CronJob had nothing in this window.', '{n} CronJobs had nothing in this window.')}` : ''}`} />
          </>
        )}
      </section>

      <div className="two">
        <section aria-label={t('Exceptions')} className="rows">
          <h2 className="label section-title">{t('Exceptions')} <span className="note">· {t('grouped, newest first')}</span></h2>
          {data.exceptions.length === 0 && <div className="empty">{data.gaps.length ? t('Nothing needs a look, as far as OpsTide could see.') : t('Nothing needs a look.')}</div>}
          {data.exceptions.map((e, i) => <ExceptionRow key={i} e={e} tz={tz} from={data.from} to={data.to} nowMs={nowMs} go={go} />)}
        </section>

        <section aria-label={t('Coming up')} className="rows">
          <h2 className="label section-title">{t('Coming up')} <span className="note">· {t('from the schedules')}</span></h2>
          {!hasNow && Date.parse(data.to) < nowMs && <div className="empty">{t('Shown for windows that reach now.')}</div>}
          {(hasNow || Date.parse(data.to) > nowMs) && data.upcoming.length === 0 && <div className="empty">{t('Nothing more is expected in this window.')}</div>}
          {data.upcoming.map((u) => (
            <Link key={u.operationId} className="row" href={`/operations/${encodeURIComponent(u.operationId)}`}>
              <span className="mono">{hhmm(u.at, tz)}</span>
              <span>{u.name}</span>
              <span className="mono muted" style={{ fontSize: 12 }}>{u.namespace}</span>
            </Link>
          ))}
          <Link href="/schedules" style={{ paddingTop: 12, fontSize: 13 }}>{t('All schedules')}</Link>
        </section>
      </div>
      <RecentChanges tz={tz} />
    </div>
  )
}

// The last 24 hours across everything OpsTide records: counts of records by kind, each a link to the records, and the
// watches that were not whole. A zero means nothing of that kind was recorded, which is only as good as the watches.
function LastDay() {
  const { data, error } = useApi<ExplorerSummary>('/api/v1/explorer/summary?range=24h', { refreshMs: 60_000 })
  if (error && !data) return <section aria-label={t('Last 24 hours')}><h1 className="title" style={{ fontSize: 28 }}>{t('Last 24 hours')}</h1><div className="error" role="alert">{t('Could not load the summary:')} {error.message}</div></section>
  if (!data) return null
  const shown = data.items.filter((i) => i.count > 0)
  return (
    <section aria-label={t('Last 24 hours')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <h1 className="title" style={{ fontSize: 28 }}>{t('Last 24 hours')}</h1>
      <p className="sentence" style={{ margin: 0, fontSize: 16 }}>
        {shown.length === 0 ? t('Nothing of the kinds counted here was recorded.') : shown.map((i, k) => (
          <span key={i.key}>{k > 0 ? ' · ' : ''}<Link href={`/explorer?range=24h&${i.query}`}><b className="mono">{i.count}</b> {t(i.count === 1 ? i.label : i.plural)}</Link></span>
        ))}
      </p>
      {(data.notWhole.length > 0 || data.dropped > 0) && (
        <p className="muted" style={{ margin: 0, fontSize: 13 }}>
          {t('Not whole:')} {[...new Set(data.notWhole.map((n) => `${t(n.label)} (${t(n.state)})`))].join(', ')}{data.dropped > 0 ? `${data.notWhole.length ? '; ' : ''}${tn(data.dropped, '{n} object given up on since OpsTide started', '{n} objects given up on since OpsTide started')}` : ''}. <Link href="/explorer?range=24h&group=observation">{t('Where')}</Link> · <Link href="/system">{t('System')}</Link>
        </p>
      )}
      <Link href={explorerLink({ range: '24h' })} style={{ fontSize: 13 }}>{t('Everything recorded in the last 24 hours')}</Link>
    </section>
  )
}

// What changed in the last day, in order: revisions, reported availability, Node conditions and the Warning
// Events Kubernetes recorded. It is a list of what was reported, not a verdict: scheduled operations that failed
// are under Exceptions, and an empty list means nothing of these kinds was recorded, not that nothing is wrong.
const changeKinds = [['revisions', 'Revisions'], ['availability', 'Availability'], ['nodes', 'Nodes'], ['evidence', 'Evidence']] as const // labels go through t()
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
  return explorerLink({ q: e.resourceName ?? '', range: '7d' })
}

function RecentChanges({ tz }: { tz: string }) {
  const { data } = useApi<Entries>('/api/v1/events?within=24h&show=releases,statefulsets,daemonsets,nodes,kubernetes,counts&limit=200', { refreshMs: 60_000 })
  const [off, setOff] = useState<Set<ChangeKind>>(new Set())
  const all = (data?.entries ?? []).filter((e) => changeKind(e.type))
  const shown = all.filter((e) => !off.has(changeKind(e.type)!)).slice(0, 14)
  const count = (k: ChangeKind) => all.filter((e) => changeKind(e.type) === k).length
  return (
    <section aria-label={t('Recent operational changes')} className="rows">
      <h2 className="label section-title">{t('Recent operational changes')} <span className="note">· {t('last 24 hours, newest first')}</span></h2>
      <fieldset style={{ border: 0, padding: 0, margin: '0 0 8px' }}>
        <span className="checks">
          {changeKinds.map(([k, label]) => (
            <label key={k}><input type="checkbox" checked={!off.has(k)} onChange={() => setOff((o) => { const n = new Set(o); if (n.has(k)) n.delete(k); else n.add(k); return n })} />{t(label)} ({count(k)})</label>
          ))}
        </span>
      </fieldset>
      {data && shown.length === 0 && <div className="empty">{data.nextCursor ? t('The newest entries of these kinds are all of another kind than the ones selected; the list is limited to 200 entries, open the Log for more.') : t('No revision, availability change, Node condition or Warning Event was recorded in the last 24 hours. That says what was recorded, not that nothing else happened.')}</div>}
      {data?.nextCursor && shown.length > 0 && <span className="muted" style={{ fontSize: 12 }}>{t('Limited to the newest 200 entries of these kinds; the counts above are of those.')}</span>}
      {shown.map((e) => (
        <Link key={e.id} className="row" href={changeHref(e)} style={{ gridTemplateColumns: '60px 110px minmax(0, 1fr)' }}>
          <span className="mono">{hhmm(e.at, tz)}</span>
          <span className="label">{t(entryWord(e.type, { 'release.observed': 'Release', 'release.stalled': 'Stalled', 'release.failed': 'Failed', 'release.unavailable': 'Availability', 'statefulset.counts_changed': 'Counts', 'daemonset.counts_changed': 'Counts', 'evidence.event': 'K8s Event', 'evidence.count_changed': 'K8s Event' }))}</span>
          <span style={{ overflowWrap: 'anywhere' }}>{e.summary}</span>
        </Link>
      ))}
      <Link href={explorerLink({ range: '24h', routine: '1' })} style={{ paddingTop: 10, fontSize: 13 }}>{t('All of these in the Explorer')}</Link>
    </section>
  )
}

function ExceptionRow({ e, tz, from, to, nowMs, go }: { e: Exception; tz: string; from: string; to: string; nowMs: number; go: (s: Selection) => void }) {
  const text = exceptionText(e, tz, from, to)
  // Inside a declared maintenance window it is still listed (after the others) and still drawn as what it is: a
  // failure stays red; the window is a label beside it, not a reason to tone it down.
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
      <span className={`mono${red ? ' fail' : ' muted'}`} style={{ fontWeight: 500 }}>{text.when}</span>
      <Link href={href} style={{ display: 'flex', flexDirection: 'column', gap: 3, color: 'var(--ink)', textDecoration: 'none' }}>
        <span style={{ fontWeight: 500 }}>{text.title}</span>
        <span className="muted">{text.detail}</span>
        {e.maintenance && <span><span className="chip maint" style={{ marginLeft: 0 }} title={e.maintenance.statement}>{t('during maintenance {name}', { name: e.maintenance.name })}</span></span>}
      </Link>
      {canShow ? <button type="button" className="button" style={{ height: 28, padding: '0 10px', fontSize: 12 }} onClick={show}>{t('Show')}</button> : <span />}
    </div>
  )
}

/** A day, or a from and to; "to" can follow now. Times are read in the chosen zone. */
function CustomRange({ tz, from, to, nowMs, go }: { tz: string; from: number; to: number; nowMs: number; go: (s: Selection) => void }) {
  const [f, setF] = useState(utcToZoned(from, tz))
  const [end, setEnd] = useState(utcToZoned(to, tz))
  const [follow, setFollow] = useState(Math.abs(to - nowMs) <= 2 * MINUTE)
  const [day, setDay] = useState('')
  const a = zonedToUtc(f, tz)
  const b = follow ? nowMs : zonedToUtc(end, tz)
  let problem = ''
  if (isNaN(a) || isNaN(b)) problem = t('Enter both times.')
  else if (b - a < MIN_WINDOW) problem = t('The window must be at least 5 minutes, and end after it starts.')
  else if (b - a > MAX_WINDOW) problem = t('The window can be at most 7 days. The strip above covers longer spans.')
  return (
    <div className="customrow" role="group" aria-label={t('Custom range')}>
      <span className="label">{t('Custom')}</span>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}><span className="label">{t('From')}</span>
        <input className="control mono" type="datetime-local" value={f} onChange={(e) => setF(e.target.value)} />
      </label>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}><span className="label">{t('To')}</span>
        <input className="control mono" type="datetime-local" value={follow ? utcToZoned(nowMs, tz) : end} disabled={follow} onChange={(e) => setEnd(e.target.value)} />
      </label>
      <label style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--muted)' }}>
        <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />{t('Keep the end at now')}
      </label>
      <button type="button" className="button" disabled={!!problem} style={{ background: problem ? undefined : 'var(--ink)', color: problem ? undefined : 'var(--bg)', fontWeight: 600 }}
        onClick={() => go(selectionFor({ from: a, to: b }, nowMs))}>{t('Apply')}</button>
      <span style={{ flex: 1 }} />
      <label style={{ display: 'flex', alignItems: 'center', gap: 8 }}><span className="label">{t('Or a day')}</span>
        <input className="control mono" type="date" value={day} onChange={(e) => { setDay(e.target.value); if (e.target.value) go({ kind: 'date', date: e.target.value }) }} />
      </label>
      <span className="muted" style={{ fontSize: 12, flexBasis: '100%' }}>{problem || t('Up to 7 days. The link keeps the window, so it can be shared.')}</span>
    </div>
  )
}
