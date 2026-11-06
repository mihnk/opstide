import type { NodeDetail, NodeHistory } from '../api'
import { Failed, Loading } from '../components/Shell'
import { useApi } from '../live'
import { Link } from '../router'
import { hhmmss } from '../time'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import { explorerLink } from '../explorer'
import { EntryRow } from './Release'
import { relationWords } from './ReleaseEvidence'

// Node context: what OpsTide recorded of one Node, and the Pods that reported running on it. It is not a node
// inventory and not a capacity view: capacity, labels, taints and images are never read.
export function Node({ id, tz }: { id: string; tz: string }) {
  const { data: d, error } = useApi<NodeDetail>(`/api/v1/nodes/${encodeURIComponent(id)}`, { keep: true })
  const { data: h } = useApi<NodeHistory>(`/api/v1/nodes/${encodeURIComponent(id)}/history`)
  if (error && !d) return <div className="page"><Failed error={error} /><Link href="/workloads">← Workloads</Link></div>
  if (!d) return <div className="page"><Loading /></div>
  const n = d.node
  return (
    <div className="page" style={{ gap: 22 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <Link href="/workloads" style={{ fontSize: 13 }}>← Workloads</Link>
        <span className="label">Node</span>
        <h1 className="title" style={{ display: 'flex', gap: 14, alignItems: 'baseline', flexWrap: 'wrap' }}>
          {n.name}
          {n.status === 'removed' && <span className="chip" style={{ fontSize: 12 }}>removed</span>}
          {n.status === 'not_observed' && <span className="chip" style={{ fontSize: 12, borderStyle: 'dashed' }} title="Node observation is off, so OpsTide cannot say this Node still exists.">not observed now</span>}
          {n.origin === 'adopted' && <span className="chip" style={{ fontSize: 12 }} title="It existed before OpsTide began watching Nodes.">adopted</span>}
        </h1>
        <span className="mono muted" style={{ fontSize: 13 }}>
          first seen {hhmmss(n.firstSeenAt, tz)}{n.createdAt ? ` · created ${hhmmss(n.createdAt, tz)}` : ''}{n.deletedAt ? ` · removed ${hhmmss(n.deletedAt, tz)}` : ''}
        </span>
        {n.status === 'not_observed' && (
          <p className="banner" role="note" style={{ margin: 0 }}>
            <span className="label">Node observation disabled</span>
            <span>Last observed: <span className="mono">{hhmmss(n.lastObservedAt, tz)}</span>. What follows is what OpsTide recorded then; nothing says the Node still exists or is in this state now.</span>
          </p>
        )}
        <span><Link href={explorerLink({ node: n.id, range: '7d' })}>Records about this Node in Explorer</Link></span>
      </div>
      {d.notes.length > 0 && <div className="banner" role="note"><span className="label">Unknown</span><span>{d.notes.map((m, i) => <span key={i} style={{ display: 'block' }}>{m}</span>)}</span></div>}

      <div className="two">
        <section aria-label="Scheduling" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <h2 className="label section-title">Scheduling</h2>
          <p style={{ margin: 0 }}>
            <span className="mono">spec.unschedulable = {String(n.unschedulable)}</span>{' '}
            <span className="muted">The field alone does not say who or what set it, nor whether Pods can be scheduled here: taints and conditions are separate.</span>
          </p>
        </section>
        <section aria-label="Conditions" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <h2 className="label section-title">Conditions as Kubernetes reported</h2>
          {n.conditions.length === 0 ? <p className="muted" style={{ margin: 0 }}>No condition has been recorded.</p> : (
            <table className="data compact">
              <thead><tr><th>Condition</th><th>Status</th><th>Reason</th><th>Last transition (source)</th><th>Seen by OpsTide</th></tr></thead>
              <tbody>
                {n.conditions.map((c) => (
                  <tr key={c.type}>
                    <td className="mono">{c.type}</td>
                    <td>{c.status}</td>
                    <td style={{ overflowWrap: 'anywhere' }}>{c.reason || <span className="muted">none</span>}{c.message ? <span className="muted" style={{ display: 'block', fontSize: 12 }}>{c.message}</span> : null}</td>
                    <td className="mono">{c.lastTransitionTime ? hhmmss(c.lastTransitionTime, tz) : <span className="muted" style={{ fontFamily: 'inherit' }}>not reported</span>}</td>
                    <td className="mono">{c.observedAt ? hhmmss(c.observedAt, tz) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <p className="muted" style={{ margin: 0, fontSize: 12 }}>A condition of Unknown means Kubernetes stopped receiving status for it; it is not a statement about the machine.</p>
        </section>
      </div>

      <section aria-label="History" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <h2 className="label section-title">Recorded transitions</h2>
        {!h ? <Loading /> : h.entries.length === 0 ? <p className="muted" style={{ margin: 0 }}>No change of this Node has been recorded since OpsTide began watching it.</p> : (
          <div className="daygroup" style={{ gap: 0 }}>{h.entries.map((e) => <EntryRow key={e.id} e={e} tz={tz} />)}</div>
        )}
      </section>

      {h && h.records.length > 0 && (
        <section aria-label="Kubernetes Events" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <h2 className="label section-title">Kubernetes Events that name this Node</h2>
          <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 }}>
            {h.records.map((r) => {
              const link = r.links.find((l) => l.kind === 'Node')
              return (
                <li key={r.id}>
                  <WorkspaceToggle id={r.recordId} compact />{' '}Kubernetes reported <b>{r.reason}</b> ({r.eventType}){(r.occurredAt.last ?? r.occurredAt.first) ? ` at ${hhmmss((r.occurredAt.last ?? r.occurredAt.first)!, tz)}` : ''}
                  {r.message ? <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>: {r.message.length > 200 ? `${r.message.slice(0, 200)}…` : r.message}</span> : null}
                  {link ? <span className="muted"> · {relationWords[link.relationship][0]}{link.via ? ` (${link.via})` : ''}</span> : null}
                </li>
              )
            })}
          </ul>
        </section>
      )}

      <section aria-label="Pods on this Node" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <h2 className="label section-title">Pods observed on this Node <span className="note">· placement context, not ownership</span></h2>
        {d.pods.length === 0 ? <p className="muted" style={{ margin: 0 }}>No Pod OpsTide observed has reported running here.</p> : (
          <div className="table-wrap">
            <table className="data compact" style={{ minWidth: 640 }}>
              <thead><tr><th>Pod</th><th>Workload</th><th>Created</th><th>Ended</th></tr></thead>
              <tbody>
                {d.pods.map((p) => (
                  <tr key={p.uid}>
                    <td className="mono" style={{ overflowWrap: 'anywhere' }}>{p.namespace}/{p.name}</td>
                    <td>{p.workloadId ? <Link href={`/workloads/${encodeURIComponent(p.workloadId)}?tab=pods`}>{p.workloadKind}</Link> : <span className="muted">—</span>}</td>
                    <td className="mono">{hhmmss(p.createdAt, tz)}</td>
                    <td className="mono">{p.terminatedAt ? hhmmss(p.terminatedAt, tz) : <span className="muted" style={{ fontFamily: 'inherit' }}>—</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {d.podsTruncated && <p className="muted" style={{ margin: 0, fontSize: 12 }}>More Pods were observed than are listed.</p>}
      </section>
    </div>
  )
}
