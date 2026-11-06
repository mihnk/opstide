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
  if (error && !d) return <div className="page"><Failed error={error} /><Link href="/workloads">← Workloads</Link></div>
  if (!d) return <div className="page"><Loading /></div>
  const w = d.workload
  const st = w.state
  return (
    <div className="page" style={{ gap: 20 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <Link href="/workloads" style={{ fontSize: 13 }}>← Workloads</Link>
        <div className="head-row" style={{ gap: '10px 24px' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <span className="label">{w.kind} · {w.namespace}</span>
            <h1 className="title" style={{ display: 'flex', alignItems: 'baseline', gap: 16, flexWrap: 'wrap' }}>
              {w.name}
              {w.origin === 'adopted' && <span className="chip" style={{ fontSize: 12 }} title="It existed before OpsTide started watching.">adopted</span>}
              {w.status === 'deleted' && <span className="chip" style={{ fontSize: 12 }}>deleted</span>}
            </h1>
            <span className="mono muted" style={{ fontSize: 13 }}>{w.namespace}/{w.name}{w.deletedAt ? ` · deleted ${hhmmss(w.deletedAt, tz)}` : ''}</span>
          </div>
          <span className="grow" />
          <Link href={explorerLink({ workload: w.id, range: '7d' })} className="button">Records in Explorer</Link>
          {d.release && <Link href={`/releases/${encodeURIComponent(d.release.id)}`} className="button">Release of revision {d.release.revision}</Link>}
        </div>
        {st && (
          <p className="sentence" style={{ fontSize: 16 }}>
            <b>Revision progress:</b> <span className="mono">{st.updated} of {st.desired}</span> replicas report the update revision; update state <b>{rolloutLabel(w.kind, st.rollout)}</b>
            {st.rolloutNote && w.kind !== 'Deployment' ? <span className="muted">. {st.rolloutNote}</span> : null}.{' '}
            <b>Observed availability:</b> <span className="mono">{st.ready}</span> ready and <span className="mono">{st.available}</span> available of <span className="mono">{st.desired}</span> desired.

          </p>
        )}
      </div>
      {d.notes.length > 0 && <div className="banner" role="note"><span className="label">Unknown</span><span>{d.notes.map((n, i) => <span key={i} style={{ display: 'block' }}>{n}</span>)}</span></div>}

      <nav aria-label="Workload sections" className="tabs">
        {tabs.filter(([k]) => !w.sections || w.sections.includes(k)).map(([k, label]) => (
          <Link key={k} href={withParams(params, { tab: k === 'overview' ? null : k })} replace aria-current={tab === k ? 'true' : undefined}>{label}</Link>
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
  if (!list || list.length === 0) return <span className="muted">Kubernetes reports no conditions for this workload.</span>
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
        <Facts title="Identity" rows={[
          ['Kind', w.kind],
          ['UID', <span className="mono" title={w.id}>{short(w.id)} <span className="muted">(short)</span></span>],
          ['First recorded', <><span className="mono">{hhmmss(w.firstSeenAt, tz)}</span> <span className="muted">{w.origin === 'adopted' ? 'found already present by OpsTide; it may be much older' : w.origin === 'observed' ? 'created while OpsTide was watching' : ''}</span></>],
          ['Last observed', <><span className="mono">{hhmmss(w.lastSeenAt, tz)}</span> <span className="muted">the last time OpsTide saw it reported</span></>],
          ...(w.deletedAt ? [['Deleted', <span className="mono">{hhmmss(w.deletedAt, tz)}</span>] as [string, React.ReactNode]] : []),
        ]} />
        {st ? (
          <Facts title="Revision progress (as Kubernetes reported)" rows={[
            ['Desired', <span className="mono">{st.desired}</span>],
            ['Updated', <span className="mono">{st.updated} <span className="muted" style={{ fontFamily: 'inherit' }}>of {st.desired} on the update revision</span></span>],
            ...(w.kind !== 'Deployment' ? [['Current', <span className="mono">{st.current}{w.kind === 'DaemonSet' ? ' scheduled' : ''}</span>] as [string, React.ReactNode]] : []),
          ]} />
        ) : <p className="muted">Kubernetes' status for this workload has not been recorded yet.</p>}
        {st && (
          <Facts title="Observed availability (as Kubernetes reported)" rows={[
            ['Ready', <span className="mono">{st.ready} <span className="muted" style={{ fontFamily: 'inherit' }}>of {st.desired}</span></span>],
            ['Available', <span className="mono">{st.available} <span className="muted" style={{ fontFamily: 'inherit' }}>of {st.desired}</span></span>],
            ...(w.kind === 'DaemonSet' ? [['Misscheduled', <span className="mono">{st.misscheduled ?? 0}</span>] as [string, React.ReactNode]] : []),
            ['Generation', <span className="mono">{st.generation} <span className="muted">(observed {st.observedGeneration})</span></span>],
          ]} />
        )}
      </div>
      {st && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
          <Facts title="Revision and update strategy" rows={[
            ['Revision', <span className="mono">{st.revision || '—'}</span>],
            ...(st.updateRevision && st.updateRevision !== st.revision ? [['Update revision', <span className="mono">{st.updateRevision}</span>] as [string, React.ReactNode]] : []),
            ['Strategy', <><span className="mono">{st.strategy || '—'}</span>{st.strategy === 'OnDelete' ? <span className="muted"> · a changed template replaces no Pod until it is deleted</span> : null}</>],
            ...(st.partition !== null && w.kind === 'StatefulSet' ? [['Partition', <span className="mono">{st.partition}{st.partition > 0 ? <span className="muted" style={{ fontFamily: 'inherit' }}> · Pods with a lower ordinal stay on the previous revision by design</span> : null}</span>] as [string, React.ReactNode]] : []),
            ...(st.maxUnavailable ? [['maxUnavailable', <span className="mono">{st.maxUnavailable}</span>] as [string, React.ReactNode]] : []),
            ...(st.maxSurge ? [['maxSurge', <span className="mono">{st.maxSurge}</span>] as [string, React.ReactNode]] : []),
            ...(st.podManagement ? [['Pod management', <span className="mono">{st.podManagement}</span>] as [string, React.ReactNode]] : []),
          ]} />
          <Facts title="Update state" rows={[
            ['State', <span><b>{rolloutLabel(w.kind, st.rollout)}</b></span>],
            ['Derived from', <span className="muted">{w.kind === 'Deployment' ? 'the Deployment’s release (see its page)' : 'Kubernetes’ status fields by a documented rule (docs/M2.7-WORKLOADS.md); it is not a health judgement'}</span>],
            ...(st.rolloutNote && w.kind !== 'Deployment' ? [['Values used', <span style={{ overflowWrap: 'anywhere' }}>{st.rolloutNote}</span>] as [string, React.ReactNode]] : []),
            ['Last meaningful change', <>
              <span className="mono">{hhmmss(st.lastChangeAt, tz)}</span>{' '}
              <span className="muted">{st.changeBasis === 'baseline' ? 'a baseline: the newest time the records or the source give for something already there, not a change OpsTide saw' : 'the last time OpsTide saw this state change'}</span>
            </>],
          ]} />
          <Facts title="Conditions Kubernetes reports" rows={[['Conditions', <Conditions list={st.conditions} />]]} />
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
      <label style={{ marginBottom: 10, fontSize: 13 }}><input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> Include routine Pod entries (created, ready, terminated)</label>
      {entries.length === 0 && <div className="empty">Nothing has been recorded for this workload yet.</div>}
      {entries.map((e) => <EntryRow key={e.id} e={e} tz={tz} />)}
      {cursor && <button type="button" className="button" style={{ alignSelf: 'flex-start', marginTop: 12 }} disabled={busy} onClick={more}>{busy ? 'Loading…' : 'Older entries'}</button>}
      <p className="muted" style={{ fontSize: 12 }}>What Kubernetes reported about this workload, in order. <Link href={explorerLink({ workload: id, routine: '1', range: '7d' })}>Open these in the Explorer</Link></p>
    </div>
  )
}

function Revisions({ id, tz }: { id: string; tz: string }) {
  const { data, error } = useApi<WorkloadRevisions>(`/api/v1/workloads/${encodeURIComponent(id)}/revisions`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  if (data.revisions.length === 0) return <div className="empty">No revision has been recorded for this workload.</div>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
      {data.notes.map((n, i) => <p key={i} className="muted" style={{ margin: 0, maxWidth: 780 }}>{n}</p>)}
      {data.revisions.map((r) => <Revision key={r.id} r={r} tz={tz} />)}
      <Note block more="Only an allow-list of the Pod template is compared: container names and images, resources, probes, the names (never the values) of environment variables, ConfigMaps and Secrets, and the service account. A revision number means something only within its own workload.">What is compared</Note>
    </div>
  )
}

function Revision({ r, tz }: { r: RevisionView; tz: string }) {
  return (
    <section aria-label={r.name} style={{ display: 'flex', flexDirection: 'column', gap: 6, borderTop: '1px solid var(--rule)', paddingTop: 12 }}>
      <div style={{ display: 'flex', gap: 10, alignItems: 'baseline', flexWrap: 'wrap' }}>
        <b className="mono">{r.name}</b>
        <span className="muted mono">#{r.number}</span>
        {r.isCurrent && <span className="chip">current</span>}
        {r.isUpdate && !r.isCurrent && <span className="chip">update revision</span>}
        {r.origin === 'adopted' && <span className="chip" title="Already there when OpsTide began watching; its template is complete either way.">adopted</span>}
        <span className="muted" style={{ fontSize: 12 }}>{r.createdAt ? `created ${hhmmss(r.createdAt, tz)} · ` : ''}first seen by OpsTide {hhmmss(r.observedAt, tz)}</span>
        {r.releaseId && <Link href={`/releases/${encodeURIComponent(r.releaseId)}`} style={{ fontSize: 13 }}>Open the release</Link>}
      </div>
      <span className="mono" style={{ fontSize: 13, overflowWrap: 'anywhere' }}>{r.images.join(' · ') || 'no image recorded'}</span>
      {r.comparison.available
        ? r.comparison.changes.length === 0
          ? <span className="muted">No captured difference from the revision before it.</span>
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
      {data.pods.length === 0 ? <div className="empty">No Pod of this workload has been observed.</div> : (
        <div className="table-wrap">
          <table className="data compact" style={{ minWidth: 820 }}>
            <thead><tr>{ordinal && <th>Ordinal</th>}<th>Pod</th><th>Node</th><th>Revision</th><th>State</th><th>Restarts</th><th>Created</th><th>Ended</th></tr></thead>
            <tbody>
              {data.pods.map((p) => (
                <tr key={p.uid}>
                  {ordinal && <td className="mono">{p.ordinal ?? <span className="muted">—</span>}</td>}
                  <td className="mono" style={{ overflowWrap: 'anywhere' }}>{p.name}</td>
                  <td className="mono">{p.node ? (p.nodeId ? <Link href={`/nodes/${encodeURIComponent(p.nodeId)}`}>{p.node}</Link> : p.node) : <span className="muted">not scheduled when seen</span>}</td>
                  <td className="mono" style={{ overflowWrap: 'anywhere' }}>{p.revision || '—'}{p.revisionRole === 'update' ? <span className="chip" style={{ marginLeft: 6 }}>update</span> : p.revisionRole === 'current' ? <span className="chip" style={{ marginLeft: 6 }}>older</span> : null}</td>
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
      {data.truncated && <p className="muted" style={{ margin: 0, fontSize: 12 }}>More Pods were observed than are listed.</p>}
      {data.nodesWithoutPod && data.nodesWithoutPod.length > 0 && (
        <section aria-label="Nodes without a recorded Pod" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <h2 className="label section-title">Nodes with no Pod of this DaemonSet recorded</h2>
          <span className="mono">{data.nodesWithoutPod.map((n, i) => <span key={n.nodeId}>{i > 0 ? ', ' : ''}<Link href={`/nodes/${encodeURIComponent(n.nodeId)}`}>{n.name}</Link></span>)}</span>
        </section>
      )}
      {data.notes.map((n, i) => <p key={i} className="muted" style={{ margin: 0, fontSize: 13, maxWidth: 780 }}>{n}</p>)}
      <Note block more="The Node is where each Pod reported it ran: placement context, not ownership. Pods OpsTide did not observe are not listed.">Where each Pod reported it ran</Note>
    </div>
  )
}

function EvidenceTab({ id, tz }: { id: string; tz: string }) {
  const { data, error } = useApi<WorkloadEvidence>(`/api/v1/workloads/${encodeURIComponent(id)}/evidence`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }}>
      {data.provenance.notes.length > 0 && <div className="banner" role="note"><span className="label">Unknown</span><span>{data.provenance.notes.map((n, i) => <span key={i} style={{ display: 'block' }}>{n}</span>)}</span></div>}
      <section aria-label="Conditions" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">Conditions Kubernetes reports</h2>
        <Conditions list={data.conditions} />
      </section>
      <section aria-label="Kubernetes Events" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">Kubernetes Events <span className="note">· about this workload or the Pods it owns</span></h2>
        {data.records.length === 0 ? <div className="empty">No Kubernetes Event is recorded for this workload.</div> : (
          <div className="table-wrap">
            <table className="data compact" style={{ minWidth: 860 }}>
              <thead><tr><th>Kubernetes reported</th><th>Related resource</th><th>Message</th><th>Count</th><th>Link</th><th>Source</th><th><span className="sr">Workspace</span></th></tr></thead>
              <tbody>
                {data.records.map((e) => {
                  const when = e.occurredAt.last ?? e.occurredAt.first
                  const link = e.links.find((l) => l.kind === 'Workload') ?? e.links[0]
                  return (
                    <tr key={e.id}>
                      <td><b>{e.reason || <span className="muted">none</span>}</b><span className="muted mono" style={{ display: 'block', fontSize: 12 }}>{e.eventType} · {when ? hhmmss(when, tz) : 'no time reported'}</span></td>
                      <td><span className="label">{e.resource.kind}</span><span className="mono" style={{ display: 'block', overflowWrap: 'anywhere' }}>{e.resource.name || '—'}</span></td>
                      <td style={{ maxWidth: 340, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{e.message ? (e.message.length > 200 ? `${e.message.slice(0, 200)}…` : e.message) : <span className="muted">no message reported</span>}</td>
                      <td className="mono">{e.count ? e.count.value : <span className="muted" style={{ fontFamily: 'inherit' }}>not reported</span>}</td>
                      <td>{link ? <span title={`${relationWords[link.relationship][1]}${link.via ? ` (${link.via})` : ''}`}><span className="label">{relationWords[link.relationship][0]}</span></span> : null}</td>
                      <td style={{ fontSize: 12 }}><span className="mono">{e.source}</span>{e.origin === 'adopted' && <span className="chip" style={{ marginLeft: 6 }}>adopted</span>}<span className="muted" style={{ display: 'block' }}>first seen {relativeTime(e.observedAt, new Date().toISOString(), tz)}</span></td>
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
