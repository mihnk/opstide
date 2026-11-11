import { useState } from 'react'
import { getJSON, type Entries, type Entry, type RevisionView, type WorkloadDetail, type WorkloadEvidence, type WorkloadPods, type WorkloadRevisions, type WorkloadState } from '../api'
import { Failed, Loading } from '../components/Shell'
import { useApi } from '../live'
import { Link, withParams } from '../router'
import { hhmmss, relativeTime } from '../time'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import { explorerLink } from '../explorer'
import { EntryRow } from './Release'
import { ScalingContext, relationWords } from './ReleaseEvidence'
import { rolloutLabel } from './Workloads'
import { Note } from '../components/Note'
import { tParts } from '../components/status'
import { t } from '../i18n'

const tabs = [['overview', 'Overview'], ['history', 'History'], ['revisions', 'Revisions'], ['pods', 'Pods'], ['evidence', 'Evidence']] as const

function Facts({ title, rows }: { title: string; rows: [string, React.ReactNode][] }) {
  return (
    <section aria-label={title} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <h2 className="label section-title">{title}</h2>
      <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
        {rows.map(([k, v]) => <div key={k} style={{ display: 'contents' }}><dt className="label">{k}</dt><dd>{v}</dd></div>)}
      </dl>
    </section>
  )
}

export function Workload({ id, params, tz }: { id: string; params: URLSearchParams; tz: string }) {
  const tab = tabs.find(([k]) => k === params.get('tab'))?.[0] ?? 'overview'
  const { data: d, error } = useApi<WorkloadDetail>(`/api/v1/workloads/${encodeURIComponent(id)}`, { keep: true })
  if (error && !d) return <div className="page"><Failed error={error} /><Link href="/workloads">← {t('Workloads')}</Link></div>
  if (!d) return <div className="page"><Loading /></div>
  const w = d.workload
  const st = w.state
  return (
    <div className="page" style={{ gap: 20 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <Link href="/workloads" style={{ fontSize: 13 }}>← {t('Workloads')}</Link>
        <div className="head-row" style={{ gap: '10px 24px' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <span className="label">{w.kind} · {w.namespace}</span>
            <h1 className="title" style={{ display: 'flex', alignItems: 'baseline', gap: 16, flexWrap: 'wrap' }}>
              {w.name}
              {w.origin === 'adopted' && <span className="chip" style={{ fontSize: 12 }} title={t('It existed before OpsTide started watching.')}>{t('adopted')}</span>}
              {w.status === 'deleted' && <span className="chip" style={{ fontSize: 12 }}>{t('deleted')}</span>}
            </h1>
            <span className="mono muted" style={{ fontSize: 13 }}>{w.namespace}/{w.name}{w.deletedAt ? ` · ${t('deleted {time}', { time: hhmmss(w.deletedAt, tz) })}` : ''}</span>
          </div>
          <span className="grow" />
          <Link href={explorerLink({ workload: w.id, range: '7d' })} className="button">{t('Records in Explorer')}</Link>
          {d.release && <Link href={`/releases/${encodeURIComponent(d.release.id)}`} className="button">{t('Release of revision {n}', { n: d.release.revision })}</Link>}
        </div>
        {st && (
          <p className="sentence" style={{ fontSize: 16 }}>
            <b>{t('Revision progress:')}</b>{' '}
            {tParts('{progress} replicas report the update revision; update state {state}', {
              progress: <span className="mono">{t('{a} of {b}', { a: st.updated, b: st.desired })}</span>,
              state: <b>{rolloutLabel(w.kind, st.rollout)}</b>,
            })}
            {st.rolloutNote && w.kind !== 'Deployment' ? <span className="muted">. {st.rolloutNote}</span> : null}.{' '}
            <b>{t('Observed availability:')}</b>{' '}
            {tParts('{ready} ready and {available} available of {desired} desired.', {
              ready: <span className="mono">{st.ready}</span>,
              available: <span className="mono">{st.available}</span>,
              desired: <span className="mono">{st.desired}</span>,
            })}

          </p>
        )}
      </div>
      {d.notes.length > 0 && <div className="banner" role="note"><span className="label">{t('Unknown')}</span><span>{d.notes.map((n, i) => <span key={i} style={{ display: 'block' }}>{n}</span>)}</span></div>}

      <nav aria-label={t('Workload sections')} className="tabs">
        {tabs.filter(([k]) => !w.sections || w.sections.includes(k)).map(([k, label]) => (
          <Link key={k} href={withParams(params, { tab: k === 'overview' ? null : k })} replace aria-current={tab === k ? 'true' : undefined}>{t(label)}</Link>
        ))}
      </nav>

      {tab === 'overview' && <Overview d={d} tz={tz} />}
      {tab === 'history' && <History id={id} tz={tz} />}
      {tab === 'revisions' && <Revisions id={id} tz={tz} />}
      {tab === 'pods' && <Pods id={id} tz={tz} kind={w.kind} />}
      {tab === 'evidence' && <EvidenceTab id={id} tz={tz} />}
    </div>
  )
}

function Conditions({ list }: { list: WorkloadState['conditions'] }) {
  if (!list || list.length === 0) return <span className="muted">{t('Kubernetes reports no conditions for this workload.')}</span>
  return (
    <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 }}>
      {list.map((c) => <li key={c.type}><span className="mono">{c.type}</span> {c.status}{c.reason ? <> · {c.reason}</> : null}{c.message ? <span className="muted" style={{ overflowWrap: 'anywhere' }}> · {c.message}</span> : null}</li>)}
    </ul>
  )
}

function Overview({ d, tz }: { d: WorkloadDetail; tz: string }) {
  const w = d.workload
  const st = w.state
  const short = (s: string) => s.slice(0, 8)
  return (
    <div className="two">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
        <Facts title={t('Identity')} rows={[
          [t('Kind'), w.kind],
          ['UID', <span className="mono" title={w.id}>{short(w.id)} <span className="muted">{t('(short)')}</span></span>],
          [t('First recorded'), <><span className="mono">{hhmmss(w.firstSeenAt, tz)}</span> <span className="muted">{w.origin === 'adopted' ? t('found already present by OpsTide; it may be much older') : w.origin === 'observed' ? t('created while OpsTide was watching') : ''}</span></>],
          [t('Last observed'), <><span className="mono">{hhmmss(w.lastSeenAt, tz)}</span> <span className="muted">{t('the last time OpsTide saw it reported')}</span></>],
          ...(w.deletedAt ? [[t('Deleted'), <span className="mono">{hhmmss(w.deletedAt, tz)}</span>] as [string, React.ReactNode]] : []),
        ]} />
        {st ? (
          <Facts title={t('Revision progress (as Kubernetes reported)')} rows={[
            [t('Desired'), <span className="mono">{st.desired}</span>],
            [t('Updated'), <span className="mono">{st.updated} <span className="muted" style={{ fontFamily: 'inherit' }}>{t('of {n} on the update revision', { n: st.desired })}</span></span>],
            ...(w.kind !== 'Deployment' ? [[t('Current'), <span className="mono">{st.current}{w.kind === 'DaemonSet' ? ` ${t('scheduled')}` : ''}</span>] as [string, React.ReactNode]] : []),
          ]} />
        ) : <p className="muted">{t("Kubernetes' status for this workload has not been recorded yet.")}</p>}
        {st && (
          <Facts title={t('Observed availability (as Kubernetes reported)')} rows={[
            [t('Ready'), <span className="mono">{st.ready} <span className="muted" style={{ fontFamily: 'inherit' }}>{t('of {n}', { n: st.desired })}</span></span>],
            [t('Available'), <span className="mono">{st.available} <span className="muted" style={{ fontFamily: 'inherit' }}>{t('of {n}', { n: st.desired })}</span></span>],
            ...(w.kind === 'DaemonSet' ? [[t('Misscheduled'), <span className="mono">{st.misscheduled ?? 0}</span>] as [string, React.ReactNode]] : []),
            [t('Generation'), <span className="mono">{st.generation} <span className="muted">{t('(observed {n})', { n: st.observedGeneration })}</span></span>],
          ]} />
        )}
      </div>
      {st && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
          <Facts title={t('Revision and update strategy')} rows={[
            [t('Revision'), <span className="mono">{st.revision || '—'}</span>],
            ...(st.updateRevision && st.updateRevision !== st.revision ? [[t('Update revision'), <span className="mono">{st.updateRevision}</span>] as [string, React.ReactNode]] : []),
            [t('Strategy'), <><span className="mono">{st.strategy || '—'}</span>{st.strategy === 'OnDelete' ? <span className="muted"> · {t('a changed template replaces no Pod until it is deleted')}</span> : null}</>],
            ...(st.partition !== null && w.kind === 'StatefulSet' ? [[t('Partition'), <span className="mono">{st.partition}{st.partition > 0 ? <span className="muted" style={{ fontFamily: 'inherit' }}> · {t('Pods with a lower ordinal stay on the previous revision by design')}</span> : null}</span>] as [string, React.ReactNode]] : []),
            ...(st.maxUnavailable ? [['maxUnavailable', <span className="mono">{st.maxUnavailable}</span>] as [string, React.ReactNode]] : []),
            ...(st.maxSurge ? [['maxSurge', <span className="mono">{st.maxSurge}</span>] as [string, React.ReactNode]] : []),
            ...(st.podManagement ? [[t('Pod management'), <span className="mono">{st.podManagement}</span>] as [string, React.ReactNode]] : []),
          ]} />
          <Facts title={t('Update state')} rows={[
            [t('State'), <span><b>{rolloutLabel(w.kind, st.rollout)}</b></span>],
            [t('Derived from'), <span className="muted">{w.kind === 'Deployment' ? t('the Deployment’s release (see its page)') : t('Kubernetes’ status fields by a documented rule (docs/M2.7-WORKLOADS.md); it is not a health judgement')}</span>],
            ...(st.rolloutNote && w.kind !== 'Deployment' ? [[t('Values used'), <span style={{ overflowWrap: 'anywhere' }}>{st.rolloutNote}</span>] as [string, React.ReactNode]] : []),
            [t('Last meaningful change'), <>
              <span className="mono">{hhmmss(st.lastChangeAt, tz)}</span>{' '}
              <span className="muted">{st.changeBasis === 'baseline' ? t('a baseline: the newest time the records or the source give for something already there, not a change OpsTide saw') : t('the last time OpsTide saw this state change')}</span>
            </>],
          ]} />
          <Facts title={t('Conditions Kubernetes reports')} rows={[[t('Conditions'), <Conditions list={st.conditions} />]]} />
        </div>
      )}
      {w.kind === 'Deployment' && <div style={{ gridColumn: '1 / -1' }}><ScalingContext workloadId={w.id} tz={tz} windowEntries={0} /></div>}
    </div>
  )
}

function History({ id, tz }: { id: string; tz: string }) {
  const [all, setAll] = useState(false)
  const path = `/api/v1/workloads/${encodeURIComponent(id)}/history?limit=100${all ? '&pods=all' : ''}`
  const { data, error } = useApi<Entries>(path)
  const [older, setOlder] = useState<{ base: string; entries: Entry[]; cursor?: string }>({ base: path, entries: [] })
  const [busy, setBusy] = useState(false)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  const seen = new Set(data.entries.map((e) => e.id))
  const entries = [...data.entries, ...(older.base === path ? older.entries : []).filter((e) => !seen.has(e.id))]
  const cursor = older.base === path && older.entries.length ? older.cursor : data.nextCursor
  const more = async () => {
    if (!cursor) return
    setBusy(true)
    try {
      const p = await getJSON<Entries>(`${path}&cursor=${encodeURIComponent(cursor)}`)
      setOlder((o) => ({ base: path, entries: [...(o.base === path ? o.entries : []), ...p.entries], cursor: p.nextCursor ?? '' }))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="daygroup" style={{ gap: 0 }}>
      <label style={{ marginBottom: 10, fontSize: 13 }}><input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> {t('Include routine Pod entries (created, ready, terminated)')}</label>
      {entries.length === 0 && <div className="empty">{t('Nothing has been recorded for this workload yet.')}</div>}
      {entries.map((e) => <EntryRow key={e.id} e={e} tz={tz} />)}
      {cursor && <button type="button" className="button" style={{ alignSelf: 'flex-start', marginTop: 12 }} disabled={busy} onClick={more}>{busy ? t('Loading…') : t('Older entries')}</button>}
      <p className="muted" style={{ fontSize: 12 }}>{t('What Kubernetes reported about this workload, in order.')} <Link href={explorerLink({ workload: id, routine: '1', range: '7d' })}>{t('Open these in the Explorer')}</Link></p>
    </div>
  )
}

function Revisions({ id, tz }: { id: string; tz: string }) {
  const { data, error } = useApi<WorkloadRevisions>(`/api/v1/workloads/${encodeURIComponent(id)}/revisions`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  if (data.revisions.length === 0) return <div className="empty">{t('No revision has been recorded for this workload.')}</div>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
      {data.notes.map((n, i) => <p key={i} className="muted" style={{ margin: 0, maxWidth: 780 }}>{n}</p>)}
      {data.revisions.map((r) => <Revision key={r.id} r={r} tz={tz} />)}
      <Note block more={t('Only an allow-list of the Pod template is compared: container names and images, resources, probes, the names (never the values) of environment variables, ConfigMaps and Secrets, and the service account. A revision number means something only within its own workload.')}>{t('What is compared')}</Note>
    </div>
  )
}

function Revision({ r, tz }: { r: RevisionView; tz: string }) {
  return (
    <section aria-label={r.name} style={{ display: 'flex', flexDirection: 'column', gap: 6, borderTop: '1px solid var(--rule)', paddingTop: 12 }}>
      <div style={{ display: 'flex', gap: 10, alignItems: 'baseline', flexWrap: 'wrap' }}>
        <b className="mono">{r.name}</b>
        <span className="muted mono">#{r.number}</span>
        {r.isCurrent && <span className="chip">{t('current')}</span>}
        {r.isUpdate && !r.isCurrent && <span className="chip">{t('update revision')}</span>}
        {r.origin === 'adopted' && <span className="chip" title={t('Already there when OpsTide began watching; its template is complete either way.')}>{t('adopted')}</span>}
        <span className="muted" style={{ fontSize: 12 }}>{r.createdAt ? t('created {created} · first seen by OpsTide {seen}', { created: hhmmss(r.createdAt, tz), seen: hhmmss(r.observedAt, tz) }) : t('first seen by OpsTide {seen}', { seen: hhmmss(r.observedAt, tz) })}</span>
        {r.releaseId && <Link href={`/releases/${encodeURIComponent(r.releaseId)}`} style={{ fontSize: 13 }}>{t('Open the release')}</Link>}
      </div>
      <span className="mono" style={{ fontSize: 13, overflowWrap: 'anywhere' }}>{r.images.join(' · ') || t('no image recorded')}</span>
      {r.comparison.available
        ? r.comparison.changes.length === 0
          ? <span className="muted">{t('No captured difference from the revision before it.')}</span>
          : <ul style={{ margin: 0, paddingLeft: 18 }}>{r.comparison.changes.map((c, i) => <li key={i} className="mono" style={{ fontSize: 13, overflowWrap: 'anywhere' }}>{[c.container, c.field].filter(Boolean).join(' ')}: {c.before || '—'} → {c.after || '—'}</li>)}</ul>
        : <span className="muted">{r.comparison.reason}</span>}
    </section>
  )
}

function Pods({ id, tz, kind }: { id: string; tz: string; kind: string }) {
  const { data, error } = useApi<WorkloadPods>(`/api/v1/workloads/${encodeURIComponent(id)}/pods`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  const ordinal = kind === 'StatefulSet'
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
      {data.pods.length === 0 ? <div className="empty">{t('No Pod of this workload has been observed.')}</div> : (
        <div className="table-wrap">
          <table className="data compact" style={{ minWidth: 820 }}>
            <thead><tr>{ordinal && <th>{t('Ordinal')}</th>}<th>Pod</th><th>Node</th><th>{t('Revision')}</th><th>{t('State')}</th><th>{t('Restarts')}</th><th>{t('Created')}</th><th>{t('Ended')}</th></tr></thead>
            <tbody>
              {data.pods.map((p) => (
                <tr key={p.uid}>
                  {ordinal && <td className="mono">{p.ordinal ?? <span className="muted">—</span>}</td>}
                  <td className="mono" style={{ overflowWrap: 'anywhere' }}>{p.name}</td>
                  <td className="mono">{p.node ? (p.nodeId ? <Link href={`/nodes/${encodeURIComponent(p.nodeId)}`}>{p.node}</Link> : p.node) : <span className="muted">{t('not scheduled when seen')}</span>}</td>
                  <td className="mono" style={{ overflowWrap: 'anywhere' }}>{p.revision || '—'}{p.revisionRole === 'update' ? <span className="chip" style={{ marginLeft: 6 }}>{t('update')}</span> : p.revisionRole === 'current' ? <span className="chip" style={{ marginLeft: 6 }}>{t('older')}</span> : null}</td>
                  <td>{p.state}{p.termReason ? <span className="muted"> · {p.termReason}</span> : null}</td>
                  <td className="mono">{p.restarts}</td>
                  <td className="mono">{hhmmss(p.createdAt, tz)}</td>
                  <td className="mono">{p.terminatedAt ? hhmmss(p.terminatedAt, tz) : <span className="muted" style={{ fontFamily: 'inherit' }}>—</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {data.truncated && <p className="muted" style={{ margin: 0, fontSize: 12 }}>{t('More Pods were observed than are listed.')}</p>}
      {data.nodesWithoutPod && data.nodesWithoutPod.length > 0 && (
        <section aria-label={t('Nodes without a recorded Pod')} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <h2 className="label section-title">{t('Nodes with no Pod of this DaemonSet recorded')}</h2>
          <span className="mono">{data.nodesWithoutPod.map((n, i) => <span key={n.nodeId}>{i > 0 ? ', ' : ''}<Link href={`/nodes/${encodeURIComponent(n.nodeId)}`}>{n.name}</Link></span>)}</span>
        </section>
      )}
      {data.notes.map((n, i) => <p key={i} className="muted" style={{ margin: 0, fontSize: 13, maxWidth: 780 }}>{n}</p>)}
      <Note block more={t('The Node is where each Pod reported it ran: placement context, not ownership. Pods OpsTide did not observe are not listed.')}>{t('Where each Pod reported it ran')}</Note>
    </div>
  )
}

function EvidenceTab({ id, tz }: { id: string; tz: string }) {
  const { data, error } = useApi<WorkloadEvidence>(`/api/v1/workloads/${encodeURIComponent(id)}/evidence`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }}>
      {data.provenance.notes.length > 0 && <div className="banner" role="note"><span className="label">{t('Unknown')}</span><span>{data.provenance.notes.map((n, i) => <span key={i} style={{ display: 'block' }}>{n}</span>)}</span></div>}
      <section aria-label={t('Conditions')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">{t('Conditions Kubernetes reports')}</h2>
        <Conditions list={data.conditions} />
      </section>
      <section aria-label={t('Kubernetes Events')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">{t('Kubernetes Events')} <span className="note">· {t('about this workload or the Pods it owns')}</span></h2>
        {data.records.length === 0 ? <div className="empty">{t('No Kubernetes Event is recorded for this workload.')}</div> : (
          <div className="table-wrap">
            <table className="data compact" style={{ minWidth: 860 }}>
              <thead><tr><th>{t('Kubernetes reported')}</th><th>{t('Related resource')}</th><th>{t('Message')}</th><th>{t('Count')}</th><th>{t('Link')}</th><th>{t('Source')}</th><th><span className="sr">{t('Workspace')}</span></th></tr></thead>
              <tbody>
                {data.records.map((e) => {
                  const when = e.occurredAt.last ?? e.occurredAt.first
                  const link = e.links.find((l) => l.kind === 'Workload') ?? e.links[0]
                  return (
                    <tr key={e.id}>
                      <td><b>{e.reason || <span className="muted">{t('none')}</span>}</b><span className="muted mono" style={{ display: 'block', fontSize: 12 }}>{e.eventType} · {when ? hhmmss(when, tz) : t('no time reported')}</span></td>
                      <td><span className="label">{e.resource.kind}</span><span className="mono" style={{ display: 'block', overflowWrap: 'anywhere' }}>{e.resource.name || '—'}</span></td>
                      <td style={{ maxWidth: 340, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{e.message ? (e.message.length > 200 ? `${e.message.slice(0, 200)}…` : e.message) : <span className="muted">{t('no message reported')}</span>}</td>
                      <td className="mono">{e.count ? e.count.value : <span className="muted" style={{ fontFamily: 'inherit' }}>{t('not reported')}</span>}</td>
                      <td>{link ? <span title={`${t(relationWords[link.relationship][1])}${link.via ? ` (${link.via})` : ''}`}><span className="label">{t(relationWords[link.relationship][0])}</span></span> : null}</td>
                      <td style={{ fontSize: 12 }}><span className="mono">{e.source}</span>{e.origin === 'adopted' && <span className="chip" style={{ marginLeft: 6 }}>{t('adopted')}</span>}<span className="muted" style={{ display: 'block' }}>{t('first seen {when}', { when: relativeTime(e.observedAt, new Date().toISOString(), tz) })}</span></td>
                      <td><WorkspaceToggle id={e.recordId} compact /></td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  )
}
