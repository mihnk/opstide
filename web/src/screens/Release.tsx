import { Fragment, useState } from 'react'
import { getJSON, query, type Entries, type Entry, type ReleaseChanges, type ReleaseDetail, type ReleasePods, type PodView } from '../api'
import { HealthGlyph, HealthStatus, RolloutGlyph, RolloutStatus, healthWords, rolloutWords, tParts } from '../components/status'
import { locale, t, tc, tn } from '../i18n'
import { Failed, Loading } from '../components/Shell'
import { useApi } from '../live'
import { entryWord } from '../entryWords'
import { Link, navigate, withParams } from '../router'
import { duration, hhmm, hhmmss, relativeTime } from '../time'
import { ZoneSelect } from './common'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import { explorerLink, rangeAround, recordIdOf } from '../explorer'
import { Evidence, RecordedEvidence } from './ReleaseEvidence'
import { Note } from '../components/Note'

const tabs = [['overview', 'Overview'], ['timeline', 'Timeline'], ['pods', 'Pods'], ['evidence', 'Evidence'], ['changes', 'Changes'], ['context', 'Investigation context']] as const

export function Release({ id, params, tz, setTz }: { id: string; params: URLSearchParams; tz: string; setTz: (z: string) => void }) {
  const tab = tabs.find(([k]) => k === params.get('tab'))?.[0] ?? 'overview'
  const windowParam = params.get('window') ?? ''
  const { data: d, error } = useApi<ReleaseDetail>(`/api/v1/releases/${encodeURIComponent(id)}${query({ window: windowParam })}`, { keep: true })
  if (error && !d) return <div className="page"><Failed error={error} /><Link href="/releases">← {t('Releases')}</Link></div>
  if (!d) return <div className="page"><Loading /></div>
  const r = d.release
  const a = d.assessment
  return (
    <div className="page" style={{ gap: 20 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <Link href="/releases" style={{ fontSize: 13 }}>← {t('Releases')}</Link>
        <div className="head-row" style={{ gap: '10px 24px' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <span className="label">Deployment · {r.namespace}</span>
            <h1 className="title" style={{ display: 'flex', alignItems: 'baseline', gap: 16, flexWrap: 'wrap' }}>
              {r.workload}
              <span className="mono" style={{ fontSize: 20, fontWeight: 500, fontStretch: '100%' }}>{t('Revision {n}', { n: r.revision })}</span>
              {r.origin === 'adopted' && <span className="chip" title={t('It existed before OpsTide started watching.')}>{t('adopted')}</span>}
            </h1>
            <span className="mono muted" style={{ fontSize: 13 }}>{d.workload.namespace}/{d.workload.name}{d.workload.deletedAt ? ` · ${t('Deployment deleted')}` : ''}</span>
          </div>
          <span className="grow" />
          <ZoneSelect tz={tz} setTz={setTz} />
          <Link className="button" style={{ display: 'inline-flex', alignItems: 'center' }} href={explorerLink({ release: r.id, ...rangeAround(r.startedAt ?? r.observedAt, r.supersededAt ?? undefined) })}>{t('Records in Explorer')}</Link>
        </div>
        <p className="sentence" style={{ fontSize: 16 }}>
          <RolloutGlyph state={r.state} />{' '}
          {tParts(r.completedAt ? '{state} at {time} with {count} available' : '{state} with {count} available', {
            state: t(rolloutWords[r.state]),
            time: r.completedAt ? <span className="mono">{hhmm(r.completedAt, tz)}</span> : null,
            count: <span className="mono">{t('{a} of {b}', { a: r.available, b: r.desired })}</span>,
          })}
          {r.origin === 'adopted'
            ? <>, <span className="muted">{t('not assessed: its rollout was not observed.')}</span></>
            : <>; {t('rule result for the observation window:')} <HealthGlyph health={a.health} /> <b style={{ color: a.health === 'degraded' ? 'var(--fail)' : undefined }}>{t(healthWords[a.health]).toLocaleLowerCase(locale())}</b>
              {a.reasons[0] && a.health !== 'healthy' ? <span className="muted">: {a.reasons[0]}</span> : null}.</>}
        </p>
      </div>

      {d.warnings.length > 0 && (
        <div className="banner" role="note"><span className="label">{t('Coverage gap')}</span><span>{d.warnings.map((w, i) => <span key={i} style={{ display: 'block' }}>{w}</span>)}</span></div>
      )}

      <nav aria-label={t('Release sections')} className="tabs">
        {tabs.map(([k, label]) => (
          <Link key={k} href={withParams(params, { tab: k === 'overview' ? null : k })} replace aria-current={tab === k ? 'true' : undefined}>{t(label)}</Link>
        ))}
      </nav>

      {tab === 'overview' && <Overview d={d} tz={tz} windowSel={Number(windowParam) || a.windowMinutes} params={params} />}
      {tab === 'timeline' && <Timeline id={id} tz={tz} />}
      {tab === 'pods' && <Pods id={id} tz={tz} now={d.now} />}
      {tab === 'evidence' && <Evidence d={d} id={id} tz={tz} />}
      {tab === 'changes' && <Changes id={id} />}
      {tab === 'context' && <Context d={d} id={id} tz={tz} />}
    </div>
  )
}

function Facts({ title, rows }: { title: string; rows: [string, React.ReactNode][] }) {
  return (
    <section aria-label={title} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <h2 className="label section-title">{title}</h2>
      <dl className="facts" style={{ gridTemplateColumns: '150px minmax(0, 1fr)' }}>
        {rows.map(([k, v]) => <Fragment key={k}><dt className="label">{k}</dt><dd>{v}</dd></Fragment>)}
      </dl>
    </section>
  )
}

function Overview({ d, tz, windowSel, params }: { d: ReleaseDetail; tz: string; windowSel: number; params: URLSearchParams }) {
  const r = d.release
  const a = d.assessment
  const nowMs = Date.parse(d.now)
  const short = (s: string) => s.slice(0, 8)
  const open = a.health === 'still_observing' && a.windowEnd
  const spanMs = a.windowEnd && a.windowStart ? Date.parse(a.windowEnd) - Date.parse(a.windowStart) : 0
  const done = a.windowStart ? Math.min(1, Math.max(0, (nowMs - Date.parse(a.windowStart)) / Math.max(1, spanMs))) : 0
  return (
    <div className="two">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
        <Facts title={t('Workload identity')} rows={[
          [t('Workload'), <span className="mono">Deployment {d.workload.namespace}/{d.workload.name}</span>],
          ['Deployment UID', <span className="mono" title={d.workload.uid}>{short(d.workload.uid)} <span className="muted">{t('(short)')}</span></span>],
          ['ReplicaSet', <span className="mono">{d.replicaSet.name}</span>],
          ['ReplicaSet UID', <span className="mono" title={d.replicaSet.uid}>{short(d.replicaSet.uid)}</span>],
          [t('Revision'), <span className="mono">{r.revision}</span>],
          [t('Template hash'), <span className="mono">{d.replicaSet.templateHash || '—'}</span>],
          [t('Images'), <span className="mono">{(d.snapshot.containers ?? []).map((c) => c.image).join(', ')}</span>],
          ...(Object.keys(d.imageIds).length ? [[t('Image IDs'), <span className="mono" style={{ fontSize: 12 }}>{Object.entries(d.imageIds).map(([k, v]) => `${k} ${v}`).join(' · ')}</span>] as [string, React.ReactNode]] : []),
        ]} />
        <Facts title={t('Rollout lifecycle')} rows={[
          [t('State'), <RolloutStatus state={r.state} />],
          [t('Reason'), <span className="mono" style={{ fontSize: 13 }}>{r.stateReason || <span className="muted">—</span>}</span>],
          [t('Observed'), <><span className="mono">{hhmmss(r.observedAt, tz)}</span> <span className="muted">{r.origin === 'adopted' ? t('found already present by OpsTide') : t('first seen by OpsTide')}</span></>],
          [t('Started'), r.startedAt ? <span className="mono">{hhmmss(r.startedAt, tz)} <span className="muted">{t('ReplicaSet created')}</span></span> : <span className="muted">{t('unknown')}</span>],
          [t('Completed'), r.completedAt ? <span className="mono">{hhmmss(r.completedAt, tz)}{r.startedAt ? <span className="muted"> {t('{d} after start', { d: duration((Date.parse(r.completedAt) - Date.parse(r.startedAt)) / 1000) })}</span> : null}</span> : <span className="muted">{t('not completed')}</span>],
          [t('Superseded'), r.supersededAt ? <span className="mono">{hhmmss(r.supersededAt, tz)}</span> : <span className="muted">{t('not superseded')}</span>],
          [t('Replicas'), <span className="mono">{t('{available} available · {ready} ready · {updated} updated · {desired} desired', { available: r.available, ready: r.ready, updated: r.updated, desired: r.desired })}{r.paused ? ` · ${t('paused')}` : ''}</span>],
        ]} />
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
        <section aria-label={t('Rule result')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <h2 className="label section-title">{t('Rule result')}</h2>
          <dl className="facts" style={{ gridTemplateColumns: '150px minmax(0, 1fr)' }}>
            <dt className="label">{t('Result')}</dt><dd><HealthStatus health={a.health} /></dd>
            <dt className="label">{t('Rule output')}</dt>
            <dd><ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 }}>{a.reasons.map((x, i) => <li key={i}>{x}</li>)}</ul>
              <Note block more={`${t('Evidence comes from the Kubernetes API. Container logs are not kept.')}${a.version ? ` ${t(a.version === 1 ? 'Judged by rule set {v} (every availability shortfall counts).' : 'Judged by rule set {v} (a shortfall that only follows a raised replica target is set aside, with the rule shown).', { v: a.version })}` : ''}`}>{a.version ? t('Rule set {v}', { v: a.version }) : t('Rule set')}</Note></dd>
          </dl>
        </section>
        <section aria-label={t('Recorded evidence')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <h2 className="label section-title">{t('Recorded evidence')} <span className="note">· {t('facts only')}</span></h2>
          <RecordedEvidence id={d.release.id} limit={6} link={withParams(params, { tab: 'evidence' })} />
        </section>
        <section aria-label={t('Observation window')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <h2 className="label section-title">{t('Observation window')}</h2>
          <dl className="facts" style={{ gridTemplateColumns: '150px minmax(0, 1fr)' }}>
            <dt className="label">{t('Length')}</dt>
            <dd>
              <div className="seg small" role="group" aria-label={t('Observation length')}>
                {a.windows.map((w) => (
                  <button key={w} type="button" aria-pressed={windowSel === w} onClick={() => navigate(withParams(params, { window: w === r.windowMinutes ? null : String(w) }), true)}>{t('{n} min', { n: w })}</button>
                ))}
              </div>
              <span className="muted" style={{ display: 'block', fontSize: 12, marginTop: 6 }}>
                {a.stored ? t('Observed for {n} min after completion, as configured.', { n: a.windowMinutes }) : t('Judged now for {n} min from what was recorded; it was observed with {m} min.', { n: a.windowMinutes, m: r.windowMinutes })}
              </span>
            </dd>
            <dt className="label">{t('Start')}</dt><dd className="mono">{a.windowStart ? hhmmss(a.windowStart, tz) : '—'}</dd>
            <dt className="label">{t('End')}</dt><dd className="mono">{a.windowEnd ? hhmmss(a.windowEnd, tz) : <span className="muted" style={{ fontFamily: 'inherit' }}>{t('when the rollout completes')}</span>}</dd>
            <dt className="label">{t('Status')}</dt>
            <dd>
              {r.origin === 'adopted' ? <span className="muted">{t('Not applicable: the rollout was not observed.')}</span>
                : open ? <><span>{t('Open, closes {when}', { when: relativeTime(a.windowEnd!, d.now, tz) })}</span><div className="progress" aria-hidden="true"><i style={{ width: `${Math.round(done * 100)}%` }} /></div></>
                : a.windowEnd ? <span>{t('Closed {time}', { time: hhmm(a.windowEnd, tz) })}</span> : <span>{t('Waiting for the rollout to complete')}</span>}
            </dd>
            <dt className="label">{t('Note')}</dt><dd className="muted">{t('Evidence recorded later in the window can still change the result.')}</dd>
          </dl>
        </section>
      </div>
    </div>
  )
}

function useEntries(id: string) {
  const path = `/api/v1/releases/${encodeURIComponent(id)}/events?limit=200&include=evidence,scaling`
  const { data, error } = useApi<Entries>(path)
  const [older, setOlder] = useState<{ base: string; entries: Entry[]; cursor?: string }>({ base: path, entries: [] })
  const [busy, setBusy] = useState(false)
  const first = data?.entries ?? []
  const seen = new Set(first.map((e) => e.id))
  const entries = [...first, ...(older.base === path ? older.entries : []).filter((e) => !seen.has(e.id))]
  const cursor = older.base === path && older.entries.length ? older.cursor : data?.nextCursor
  const more = async () => {
    if (!cursor) return
    setBusy(true)
    try {
      const p = await getJSON<Entries>(`${path.replace('&include=evidence,scaling', '')}&cursor=${encodeURIComponent(cursor)}`)
      setOlder((o) => ({ base: path, entries: [...(o.base === path ? o.entries : []), ...p.entries], cursor: p.nextCursor ?? '' }))
    } finally {
      setBusy(false)
    }
  }
  return { data, error, entries, cursor, more, busy }
}

const typeWord: Record<string, string> = {
  'release.observed': 'Release', 'release.superseded': 'Release', 'release.scaled_down': 'Release', 'release.paused': 'Release', 'release.resumed': 'Release',
  'release.stalled': 'Stalled', 'release.failed': 'Failed', 'release.health.degraded': 'Findings', 'release.health.healthy': 'Findings', 'release.health.insufficient': 'Findings',
  'release.unavailable': 'Availability', 'deployment.rollout': 'Rollout', 'pod.created': 'Pod', 'pod.ready': 'Pod', 'pod.notready': 'NotReady', 'pod.restarted': 'Restart',
  'pod.oomkilled': 'OOMKilled', 'pod.waiting': 'Waiting', 'pod.evicted': 'Evicted', 'pod.terminated': 'Pod',
  'evidence.record': 'K8s Event', 'evidence.event': 'K8s Event', 'evidence.count_changed': 'K8s Event',
  'hpa.desired_replicas_changed': 'Scaling', 'hpa.condition_changed': 'Scaling', 'workload.replica_target_changed': 'Scaling',
}

// Which kind of entry a timeline row is, so they can be told apart and filtered.
const categories = [['release', 'Release lifecycle'], ['pod', 'Pod lifecycle'], ['event', 'Kubernetes Events'], ['scaling', 'Scaling']] as const
type Category = (typeof categories)[number][0]
function categoryOf(type: string): Category {
  if (type.startsWith('pod.')) return 'pod'
  if (type.startsWith('evidence.')) return 'event'
  if (type.startsWith('hpa.') || type.startsWith('workload.')) return 'scaling'
  return 'release'
}

export function EntryRow({ e, tz }: { e: Entry; tz: string }) {
  return (
    <div className={`entry static ${e.severity}`}>
      <span className={`when mono${e.severity === 'error' ? ' fail' : e.confidence === 'unknown' ? ' muted' : ''}`} title={`${e.at} UTC`}>{hhmmss(e.at, tz)}</span>
      <span className="label" style={{ color: e.severity === 'error' ? 'var(--fail)' : undefined }}>{t(entryWord(e.type, typeWord))}</span>
      <span style={{ minWidth: 0 }}>
        <span>{e.summary}</span>
        {e.type.startsWith('evidence.') && e.facts?.message ? <span className="ref" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }} title={e.facts.message}>{e.facts.message.length > 200 ? `${e.facts.message.slice(0, 200)}…` : e.facts.message}</span> : null}
        <span className="ref mono">{e.type.startsWith('evidence.') ? `${t('Kubernetes reported')} · ${e.facts?.eventType ?? ''}${e.facts?.count ? ` · ${t('count {n}', { n: e.facts.count })}` : ''}${e.facts?.origin === 'adopted' ? ` · ${t('already present when OpsTide began watching')}` : ''}` : e.confidence}{e.occurredAt ? '' : ` · ${t('the source gives no time')}`}</span>
      </span>
      <WorkspaceToggle id={recordIdOf(e)} compact />
    </div>
  )
}

function Timeline({ id, tz }: { id: string; tz: string }) {
  const { data, error, entries: all, cursor, more, busy } = useEntries(id)
  const [off, setOff] = useState<Set<Category>>(new Set())
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  if (all.length === 0) return <div className="empty">{t('Nothing has been recorded for this release yet.')}</div>
  const entries = all.filter((e) => !off.has(categoryOf(e.type)))
  const count = (c: Category) => all.filter((e) => categoryOf(e.type) === c).length
  // Release-level entries stand alone; a Pod's entries fold into one group, placed
  // where its latest entry falls, and open on their own when something went wrong.
  type Item = { at: number; entry?: Entry; pod?: { name: string; entries: Entry[] } }
  const pods = new Map<string, Entry[]>()
  const items: Item[] = []
  for (const e of entries) {
    if (e.type.startsWith('pod.') && e.resourceName) pods.set(e.resourceName, [...(pods.get(e.resourceName) ?? []), e])
    else items.push({ at: Date.parse(e.at), entry: e })
  }
  for (const [name, es] of pods) items.push({ at: Math.max(...es.map((e) => Date.parse(e.at))), pod: { name, entries: es } })
  items.sort((x, y) => y.at - x.at)
  return (
    <div className="daygroup" style={{ gap: 0 }}>
      <fieldset style={{ border: 0, padding: 0, margin: '0 0 12px' }}>
        <legend className="label" style={{ padding: '0 0 4px' }}>{t('Show')}</legend>
        <span className="checks">
          {categories.map(([k, label]) => (
            <label key={k}><input type="checkbox" checked={!off.has(k)} onChange={() => setOff((o) => { const n = new Set(o); if (n.has(k)) n.delete(k); else n.add(k); return n })} />{t(label)} ({count(k)})</label>
          ))}
        </span>
      </fieldset>
      {data.truncated && <p className="muted" style={{ fontSize: 12, margin: '0 0 8px' }}>{t('The newest entries are shown; the timeline was cut at its limit. Release lifecycle entries are in the Log.')}</p>}
      {entries.length === 0 && <div className="empty">{t('Every kind of entry is hidden.')}</div>}
      {items.map((it) => it.entry ? <EntryRow key={it.entry.id} e={it.entry} tz={tz} /> : (
        <details key={`pod-${it.pod!.name}`} className="pod-group" open={it.pod!.entries.some((e) => e.severity !== 'info')}>
          <summary>
            <span className="label">Pod</span> <span className="mono">{it.pod!.name}</span>
            <span className="muted"> · {tn(it.pod!.entries.length, '{n} entry', '{n} entries')}{it.pod!.entries.some((e) => e.severity === 'error') ? ` · ${t('includes an error')}` : it.pod!.entries.some((e) => e.severity === 'warning') ? ` · ${t('includes a warning')}` : ''}</span>
          </summary>
          {it.pod!.entries.map((e) => <EntryRow key={e.id} e={e} tz={tz} />)}
        </details>
      ))}
      {cursor && <button type="button" className="button" style={{ alignSelf: 'flex-start', marginTop: 12 }} disabled={busy} onClick={more}>{busy ? t('Loading…') : t('Older entries')}</button>}
      <p className="muted" style={{ fontSize: 12 }}>{t('Pod entries are folded under their Pod.')} <Link href={explorerLink({ release: id, routine: '1', range: '7d' })}>{t('Open these in the Explorer')}</Link></p>
    </div>
  )
}

function Strip({ p, now }: { p: PodView; now: string }) {
  const start = Date.parse(p.createdAt)
  const end = p.terminatedAt ? Date.parse(p.terminatedAt) : Date.parse(now)
  const span = Math.max(1, end - start)
  const segs: { from: number; to: number; s: 'pending' | 'ready' | 'notready' }[] = []
  let cur: 'pending' | 'ready' | 'notready' = 'pending'
  let at = start
  for (const t of p.readiness) {
    const x = Date.parse(t.at)
    segs.push({ from: at, to: x, s: cur })
    cur = t.ready ? 'ready' : 'notready'
    at = x
  }
  segs.push({ from: at, to: end, s: cur })
  const word = { pending: 'not yet Ready', ready: 'Ready', notready: 'NotReady' }
  const text = segs.filter((s) => s.to > s.from).map((s) => `${t(word[s.s])} ${duration((s.to - s.from) / 1000)}`).join(t(', then '))
  return (
    <span className="ready-strip" role="img" aria-label={t('Readiness: {text}', { text })} title={text}>
      {segs.map((s, i) => <i key={i} className={s.s} style={{ width: `${((s.to - s.from) / span) * 100}%` }} />)}
    </span>
  )
}

function Pods({ id, tz, now }: { id: string; tz: string; now: string }) {
  const { data, error } = useApi<ReleasePods>(`/api/v1/releases/${encodeURIComponent(id)}/pods`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <div className="muted" style={{ fontSize: 13 }}>ReplicaSet <span className="mono">{data.replicaSet.name}</span>{data.replicaSet.templateHash ? <> · {t('hash')} <span className="mono">{data.replicaSet.templateHash}</span></> : null}. {t('Pods are tied to it by UID, not by name.')}</div>
      {data.pods.length === 0 && <div className="empty">{t('No Pod of this release was observed. If it was adopted, its Pods that already existed were not replayed; they appear here when they change.')}</div>}
      {data.pods.length > 0 && (
        <div className="table-wrap">
          <table className="data compact" style={{ minWidth: 940 }}>
            <thead><tr>{['Pod', 'Node', 'Created', 'Scheduled', 'First ready', 'State', 'Restarts', 'Last termination', 'Readiness'].map((h) => <th key={h} scope="col" className="label">{tc('pod', h)}</th>)}</tr></thead>
            <tbody>
              {data.pods.map((p) => {
                const last = p.containers.filter((c) => c.lastReason || c.oomKilled)
                return (
                  <tr key={p.uid}>
                    <td><span className="mono" style={{ fontSize: 12 }}>{p.name}</span>{p.quiet && <span className="chip" title={t('It existed when its release was adopted: earlier history was not observed.')}>{t('baseline')}</span>}</td>
                    <td className="mono" style={{ fontSize: 12 }}>{p.node || '—'}</td>
                    <td className="mono">{hhmmss(p.createdAt, tz)}</td>
                    <td className="mono">{p.scheduledAt ? hhmmss(p.scheduledAt, tz) : <span className="muted">—</span>}</td>
                    <td className="mono">{p.readyAt ? hhmmss(p.readyAt, tz) : <span className="muted">{t('never')}</span>}</td>
                    <td>{p.state}{p.evicted ? ` · ${t('evicted')}` : ''}{p.terminatedAt ? <span className="muted mono" style={{ display: 'block', fontSize: 12 }}>{hhmmss(p.terminatedAt, tz)}{p.termReason && p.termReason !== 'deleted' ? ` · ${p.termReason}` : ''}</span> : null}</td>
                    <td className="mono" style={{ color: p.restarts ? 'var(--fail)' : undefined }}>{p.restarts}{p.containers.some((c) => c.waiting.length) && <span style={{ display: 'block', fontSize: 12 }}>{p.containers.flatMap((c) => c.waiting).join(', ')}</span>}</td>
                    <td style={{ fontSize: 13 }}>{last.length === 0 ? <span className="muted">{t('none observed')}</span> : last.map((c) => (
                      <span key={c.name} style={{ display: 'block', color: c.oomKilled ? 'var(--fail)' : undefined }}>{c.name}: {c.oomKilled ? 'OOMKilled' : c.lastReason}{c.lastExit != null ? `, ${t('exit {code}', { code: c.lastExit })}` : ''}</span>
                    ))}</td>
                    <td><Strip p={p} now={now} /></td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {data.truncated && <div className="muted" style={{ fontSize: 12 }}>{t('More Pods exist than are shown; the newest 500 are listed.')}</div>}
      <div className="legend" style={{ paddingTop: 0 }}>
        <span><span className="ready-strip" style={{ width: 24 }}><i className="ready" style={{ width: 24 }} /></span>{t('Ready')}</span>
        <span><span className="ready-strip" style={{ width: 24 }}><i className="notready" style={{ width: 24 }} /></span>{t('NotReady')}</span>
        <span><span className="ready-strip" style={{ width: 24 }}><i className="pending" style={{ width: 24 }} /></span>{t('not yet Ready')}</span>
        <span>{t('A restart count is not a new release; a Pod ending during a rollout is not a failure.')}</span>
      </div>
    </div>
  )
}

function Changes({ id }: { id: string }) {
  const { data, error } = useApi<ReleaseChanges>(`/api/v1/releases/${encodeURIComponent(id)}/changes`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  if (!data.available) {
    return <div className="dashed"><span className="label">{t('Comparison unavailable')}</span><p style={{ margin: '6px 0 0' }}>{data.reason}</p></div>
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
      <div className="muted" style={{ fontSize: 13 }}>{tParts('Compared with revision {rev}, the revision before it that OpsTide has a snapshot of.', { rev: <Link href={`/releases/${encodeURIComponent(data.previous!.id)}`}>{data.previous!.revision}</Link> })}</div>
      {data.changes.length === 0 && (
        <div className="dashed">
          <span className="label">{data.sameTemplate ? t('Same Pod template') : t('No captured differences')}</span>
          <p style={{ margin: '6px 0 0' }}>{data.sameTemplate
            ? t('The Pod template is the same as the previous revision’s: this release runs the same ReplicaSet (or one with the same hash), as after a rollback.')
            : t('The ReplicaSet differs, but none of the fields OpsTide captures changed. This is what `kubectl rollout restart` looks like, and also what a change to an environment value or a command looks like: OpsTide does not keep those, so something there may have changed.')}</p>
        </div>
      )}
      {data.changes.length > 0 && (
        <div className="table-wrap">
          <table className="data compact" style={{ minWidth: 700 }}>
            <thead><tr>{[t('Field'), t('Change'), t('Revision {n}', { n: data.previous!.revision }), t('This revision')].map((h) => <th key={h} scope="col" className="label">{h}</th>)}</tr></thead>
            <tbody>
              {data.changes.map((c, i) => (
                <tr key={i}>
                  <td>{c.container ? <span className="mono muted" style={{ fontSize: 12 }}>{c.container} </span> : null}{c.field}</td>
                  <td className="label">{c.kind}</td>
                  <td className="mono muted" style={{ fontSize: 13 }}>{c.before || '—'}</td>
                  <td className="mono" style={{ fontSize: 13 }}>{c.after || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {data.reason && <div className="muted" style={{ fontSize: 12 }}>{data.reason}</div>}
    </div>
  )
}

function Context({ d, id, tz }: { d: ReleaseDetail; id: string; tz: string }) {
  const { data: ch } = useApi<ReleaseChanges>(`/api/v1/releases/${encodeURIComponent(id)}/changes`)
  const r = d.release
  const a = d.assessment
  const section = (title: string, items: React.ReactNode[], none: string) => (
    <section aria-label={title} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <h2 className="label section-title">{title}</h2>
      {items.length === 0 ? <p className="muted" style={{ margin: 0 }}>{none}</p> : <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 5 }}>{items.map((x, i) => <li key={i}>{x}</li>)}</ul>}
    </section>
  )
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }}>
      <Note block more={t('What OpsTide already holds about this release, put together by fixed rules. It is not an explanation: no model reads it, and nothing outside the record is added.')}>{t('Held records, put together by fixed rules')}</Note>
      <div className="two">
        {section(t('Rollout conditions'), [
          <>{tParts('Rollout {state}', { state: <b>{t(rolloutWords[r.state]).toLocaleLowerCase(locale())}</b> })}{r.stateReason ? <>: <span className="mono">{r.stateReason}</span></> : null}.</>,
          <>{t('{available} of {desired} replicas available, {updated} updated', { available: r.available, desired: r.desired, updated: r.updated })}{r.paused ? t(', rollout paused') : ''}.</>,
          r.completedAt ? <>{t('Completed at {time}.', { time: hhmmss(r.completedAt, tz) })}</> : <>{t('Not completed.')}</>,
          ...(r.supersededAt ? [<>{t('Replaced by a newer revision at {time}.', { time: hhmmss(r.supersededAt, tz) })}</>] : []),
        ], '')}
        {section(t('Pod health changes'), a.findings.map((f) => <>{f.summary}{f.at || f.observed ? <span className="muted mono"> · {hhmmss((f.at || f.observed)!, tz)}</span> : null}</>),
          r.origin === 'adopted' ? t('Not assessed: this release was adopted.') : t('No restart, OOMKill, waiting reason, NotReady transition, eviction or lasting loss of availability was observed in the window.'))}
        {section(t('Configuration differences'), ch?.available ? ch.changes.map((c) => c.summary) : [], ch ? (ch.available ? t('No captured difference from the previous revision.') : ch.reason ?? '') : t('Loading…'))}
        {section(t('Coverage limitations'), [...(a.evidence ?? []), ...d.warnings], t('OpsTide watched Deployments, ReplicaSets and Pods without a gap while it observed this release.'))}
      </div>
      <div className="legend" style={{ paddingTop: 0 }}>
        <span>{t('Rule result:')} <HealthStatus health={a.health} /></span>
        <span><Link href={explorerLink({ release: id, routine: '1', range: '7d' })}>{t('Every record of this release in the Explorer')}</Link></span>
      </div>
    </div>
  )
}
