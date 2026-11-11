import type { NodeDetail, NodeHistory } from '../api'
import { Failed, Loading } from '../components/Shell'
import { useApi } from '../live'
import { Link } from '../router'
import { hhmmss } from '../time'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import { explorerLink } from '../explorer'
import { EntryRow } from './Release'
import { relationWords } from './ReleaseEvidence'
import { Note } from '../components/Note'
import { tParts } from '../components/status'
import { t } from '../i18n'

// Node context: what OpsTide recorded of one Node, and the Pods that reported running on it. It is not a node
// inventory and not a capacity view: capacity, labels, taints and images are never read.
export function Node({ id, tz }: { id: string; tz: string }) {
  const { data: d, error } = useApi<NodeDetail>(`/api/v1/nodes/${encodeURIComponent(id)}`, { keep: true })
  const { data: h } = useApi<NodeHistory>(`/api/v1/nodes/${encodeURIComponent(id)}/history`)
  if (error && !d) return <div className="page"><Failed error={error} /><Link href="/workloads">← {t('Workloads')}</Link></div>
  if (!d) return <div className="page"><Loading /></div>
  const n = d.node
  return (
    <div className="page" style={{ gap: 22 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <Link href="/workloads" style={{ fontSize: 13 }}>← {t('Workloads')}</Link>
        <span className="label">Node</span>
        <h1 className="title" style={{ display: 'flex', gap: 14, alignItems: 'baseline', flexWrap: 'wrap' }}>
          {n.name}
          {n.status === 'removed' && <span className="chip" style={{ fontSize: 12 }}>{t('removed')}</span>}
          {n.status === 'not_observed' && <span className="chip" style={{ fontSize: 12, borderStyle: 'dashed' }} title={t('Node observation is off, so OpsTide cannot say this Node still exists.')}>{t('not observed now')}</span>}
          {n.origin === 'adopted' && <span className="chip" style={{ fontSize: 12 }} title={t('It existed before OpsTide began watching Nodes.')}>{t('adopted')}</span>}
        </h1>
        <span className="mono muted" style={{ fontSize: 13 }}>
          {t('first seen {time}', { time: hhmmss(n.firstSeenAt, tz) })}{n.createdAt ? ` · ${t('created {time}', { time: hhmmss(n.createdAt, tz) })}` : ''}{n.deletedAt ? ` · ${t('removed {time}', { time: hhmmss(n.deletedAt, tz) })}` : ''}
        </span>
        {n.status === 'not_observed' && (
          <p className="banner" role="note" style={{ margin: 0 }}>
            <span className="label">{t('Node observation disabled')}</span>
            <span>{tParts('Last observed: {time}. What follows is what OpsTide recorded then; nothing says the Node still exists or is in this state now.', { time: <span className="mono">{hhmmss(n.lastObservedAt, tz)}</span> })}</span>
          </p>
        )}
        <span><Link href={explorerLink({ node: n.id, range: '7d' })}>{t('Records about this Node in Explorer')}</Link></span>
      </div>
      {d.notes.length > 0 && <div className="banner" role="note"><span className="label">{t('Unknown')}</span><span>{d.notes.map((m, i) => <span key={i} style={{ display: 'block' }}>{m}</span>)}</span></div>}

      <div className="two">
        <section aria-label={t('Scheduling')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <h2 className="label section-title">{t('Scheduling')}</h2>
          <div style={{ margin: 0 }}>
            <span className="mono">spec.unschedulable = {String(n.unschedulable)}</span>{' '}
            <Note more={t('The field alone does not say who or what set it, nor whether Pods can be scheduled here: taints and conditions are separate.')}>{t('as reported')}</Note>
          </div>
        </section>
        <section aria-label={t('Conditions')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <h2 className="label section-title">{t('Conditions as Kubernetes reported')}</h2>
          {n.conditions.length === 0 ? <p className="muted" style={{ margin: 0 }}>{t('No condition has been recorded.')}</p> : (
            <table className="data compact">
              <thead><tr><th>{t('Condition')}</th><th>{t('Status')}</th><th>{t('Reason')}</th><th>{t('Last transition (source)')}</th><th>{t('Seen by OpsTide')}</th></tr></thead>
              <tbody>
                {n.conditions.map((c) => (
                  <tr key={c.type}>
                    <td className="mono">{c.type}</td>
                    <td>{c.status}</td>
                    <td style={{ overflowWrap: 'anywhere' }}>{c.reason || <span className="muted">{t('none')}</span>}{c.message ? <span className="muted" style={{ display: 'block', fontSize: 12 }}>{c.message}</span> : null}</td>
                    <td className="mono">{c.lastTransitionTime ? hhmmss(c.lastTransitionTime, tz) : <span className="muted" style={{ fontFamily: 'inherit' }}>{t('not reported')}</span>}</td>
                    <td className="mono">{c.observedAt ? hhmmss(c.observedAt, tz) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <Note more={t('A condition of Unknown means Kubernetes stopped receiving status for it; it is not a statement about the machine.')} block>{t('What Unknown means')}</Note>
        </section>
      </div>

      <section aria-label={t('History')} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <h2 className="label section-title">{t('Recorded transitions')}</h2>
        {!h ? <Loading /> : h.entries.length === 0 ? <p className="muted" style={{ margin: 0 }}>{t('No change of this Node has been recorded since OpsTide began watching it.')}</p> : (
          <div className="daygroup" style={{ gap: 0 }}>{h.entries.map((e) => <EntryRow key={e.id} e={e} tz={tz} />)}</div>
        )}
      </section>

      {h && h.records.length > 0 && (
        <section aria-label={t('Kubernetes Events')} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <h2 className="label section-title">{t('Kubernetes Events that name this Node')}</h2>
          <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 }}>
            {h.records.map((r) => {
              const link = r.links.find((l) => l.kind === 'Node')
              const at = r.occurredAt.last ?? r.occurredAt.first
              const reported = { reason: <b>{r.reason}</b>, type: r.eventType, time: at ? hhmmss(at, tz) : '' }
              return (
                <li key={r.id}>
                  <WorkspaceToggle id={r.recordId} compact />{' '}{tParts(at ? 'Kubernetes reported {reason} ({type}) at {time}' : 'Kubernetes reported {reason} ({type})', reported)}
                  {r.message ? <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>: {r.message.length > 200 ? `${r.message.slice(0, 200)}…` : r.message}</span> : null}
                  {link ? <span className="muted"> · {t(relationWords[link.relationship][0])}{link.via ? ` (${link.via})` : ''}</span> : null}
                </li>
              )
            })}
          </ul>
        </section>
      )}

      <section aria-label={t('Pods on this Node')} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        <h2 className="label section-title">{t('Pods observed on this Node')} <span className="note">· {t('placement context, not ownership')}</span></h2>
        {d.pods.length === 0 ? <p className="muted" style={{ margin: 0 }}>{t('No Pod OpsTide observed has reported running here.')}</p> : (
          <div className="table-wrap">
            <table className="data compact" style={{ minWidth: 640 }}>
              <thead><tr><th>Pod</th><th>{t('Workload')}</th><th>{t('Created')}</th><th>{t('Ended')}</th></tr></thead>
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
        {d.podsTruncated && <p className="muted" style={{ margin: 0, fontSize: 12 }}>{t('More Pods were observed than are listed.')}</p>}
      </section>
    </div>
  )
}
