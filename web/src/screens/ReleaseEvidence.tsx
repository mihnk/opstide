import { useState } from 'react'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import type { EvidenceRecord, HPAView, ReleaseDetail, ReleaseEvidence, ReleaseRecord, Relationship, WorkloadScaling } from '../api'
import { Failed, Loading } from '../components/Shell'
import { useApi } from '../live'
import { Link } from '../router'
import { hhmmss } from '../time'
import { tParts } from '../components/status'
import { t, tn } from '../i18n'

// Words for the four classifications. They say how two records are linked; none of them says
// that one caused the other. The English words are the keys: draw them through t().
export const relationWords: Record<Relationship, [string, string]> = {
  DIRECT: ['Direct', 'The Event names the resource by UID.'],
  OWNERSHIP: ['Ownership', 'The resource is linked to another through Kubernetes ownerReferences.'],
  SAME_RESOURCE_CONTEXT: ['Same resource context', 'Both concern the same resource or workload. Nothing is asserted between them.'],
  UNRESOLVED: ['Unresolved', 'There is not enough evidence to link the records. OpsTide does not match by name.'],
}

function Relation({ r, via }: { r: Relationship; via?: string }) {
  const word = t(relationWords[r][0])
  const what = t(relationWords[r][1])
  return (
    <span title={`${what}${via ? ` (${via})` : ''}`}>
      <span className="label" style={{ color: r === 'UNRESOLVED' ? 'var(--fail)' : undefined }}>{word}</span>
      {via ? <span className="muted" style={{ display: 'block', fontSize: 12 }}>{via}</span> : null}
    </span>
  )
}

/** Text from the cluster is shown as text, wrapped, and long messages fold. */
function Message({ text, truncated }: { text: string; truncated?: boolean }) {
  if (!text) return <span className="muted">{t('no message reported')}</span>
  const long = text.length > 140
  const body = <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{long ? `${text.slice(0, 140)}…` : text}</span>
  if (!long && !truncated) return body
  return (
    <details>
      <summary style={{ cursor: 'pointer' }}>{body}</summary>
      <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', display: 'block', marginTop: 4 }}>{text}</span>
      {truncated && <span className="muted" style={{ fontSize: 12 }}>{t('OpsTide kept the first 1 KiB of this message.')}</span>}
    </details>
  )
}

function SourceCell({ e, tz }: { e: EvidenceRecord; tz: string }) {
  const at = (v: string | null) => (v ? hhmmss(v, tz) : t('not reported'))
  return (
    <span style={{ fontSize: 12 }}>
      <span className="mono">{e.source}</span>
      {e.origin === 'adopted' && <span className="chip" title={t('Already there when OpsTide began watching. OpsTide keeps the times Kubernetes gave; it does not date it at startup.')} style={{ marginLeft: 6 }}>{t('adopted')}</span>}
      <span className="muted" style={{ display: 'block' }}>{t('first {first} · last {last}', { first: at(e.occurredAt.first), last: at(e.occurredAt.last) })}</span>
      <span className="muted" style={{ display: 'block' }}>{t('OpsTide first saw it {time}', { time: hhmmss(e.observedAt, tz) })}{e.reporter ? ` · ${t('reported by {reporter}', { reporter: e.reporter })}` : ''}</span>
      {e.sourceDeletedAt && <span className="muted" style={{ display: 'block' }}>{t('no longer in Kubernetes since {time}; kept here', { time: hhmmss(e.sourceDeletedAt, tz) })}</span>}
    </span>
  )
}

/** The compact, facts-only list. Every line is an observed value or a documented rule applied to
 *  observed values; the cause is not part of it. */
export function RecordedEvidence({ id, limit, link }: { id: string; limit?: number; link?: string }) {
  const { data, error } = useApi<ReleaseEvidence>(`/api/v1/releases/${encodeURIComponent(id)}/evidence`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  const facts = limit ? data.summary.facts.slice(0, limit) : data.summary.facts
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4 }}>
        {facts.map((f, i) => (
          <li key={i}>{f.text}<span className="muted mono" style={{ fontSize: 11 }} title={f.refs.length ? tn(f.refs.length, 'Rule {rule}, from {n} record', 'Rule {rule}, from {n} records', { rule: f.rule }) : t('Rule {rule}', { rule: f.rule })}> · {f.rule}</span></li>
        ))}
      </ul>
      {limit && data.summary.facts.length > limit && link && <Link href={link} style={{ fontSize: 13 }}>{t('{n} more in Evidence', { n: data.summary.facts.length - limit })}</Link>}
      <p style={{ margin: 0 }}><span className="label">{t('Root cause')}</span> <b>{data.summary.rootCause}</b></p>
      {data.summary.note && <p className="muted" style={{ margin: 0, fontSize: 12 }}>{data.summary.note}</p>}
      <span className="muted" style={{ fontSize: 12 }}>{t('Built from the records on the Evidence tab by fixed rules; co-occurrence is not treated as cause.')}</span>
    </div>
  )
}

export function Evidence({ d, id, tz }: { d: ReleaseDetail; id: string; tz: string }) {
  const { data, error } = useApi<ReleaseEvidence>(`/api/v1/releases/${encodeURIComponent(id)}/evidence`)
  const [only, setOnly] = useState<'all' | 'Warning'>('all')
  const [rel, setRel] = useState<Relationship | 'all'>('all')
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  const rows = data.records.filter((r) => (only === 'all' || r.eventType === 'Warning') && (rel === 'all' || r.releaseRelationship === rel))
  const c = data.counts
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
      <p className="muted" style={{ margin: 0, maxWidth: 800 }}>
        {t("Recorded evidence: Kubernetes Events about this release's ReplicaSet and the Pods it owns, and about its Deployment while this release was current. Kubernetes reported them; OpsTide does not say why anything happened.")}
      </p>
      {data.provenance.notes.length > 0 && <div className="banner" role="note"><span className="label">{t('Unknown')}</span><span>{data.provenance.notes.map((n, i) => <span key={i} style={{ display: 'block' }}>{n}</span>)}</span></div>}

      <section aria-label={t('Recorded evidence')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">{t('Recorded evidence')}</h2>
        <RecordedEvidence id={id} />
      </section>

      <section aria-label={t('Kubernetes Events')} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <h2 className="label section-title">{t('Kubernetes Events')} <span className="note">· {t('{n} recorded', { n: data.records.length })}{data.truncated ? ` ${t('(newest shown; more exist)')}` : ''}</span></h2>
        <div className="legend" style={{ paddingTop: 0, flexWrap: 'wrap' }}>
          <span className="seg small" role="group" aria-label={t('Event type')}>
            <button type="button" aria-pressed={only === 'all'} onClick={() => setOnly('all')}>{t('All types')}</button>
            <button type="button" aria-pressed={only === 'Warning'} onClick={() => setOnly('Warning')}>{t('Warnings ({n})', { n: c.warnings ?? 0 })}</button>
          </span>
          <span className="seg small" role="group" aria-label={t('Relationship to this release')}>
            <button type="button" aria-pressed={rel === 'all'} onClick={() => setRel('all')}>{t('Any relationship')}</button>
            {(['DIRECT', 'OWNERSHIP', 'SAME_RESOURCE_CONTEXT'] as Relationship[]).map((k) => (
              <button key={k} type="button" aria-pressed={rel === k} title={t(relationWords[k][1])} onClick={() => setRel(k)}>{t(relationWords[k][0])} ({c[k] ?? 0})</button>
            ))}
          </span>
        </div>
        {rows.length === 0 ? <div className="empty">{data.records.length === 0 ? t('No Kubernetes Event was recorded for this release.') : t('No recorded Event matches this filter.')}</div> : (
          <div className="table-wrap">
            <table className="data compact" style={{ minWidth: 980 }}>
              <thead><tr><th>{t('Kubernetes reported')}</th><th>{t('Related resource')}</th><th>{t('Message')}</th><th style={{ whiteSpace: 'nowrap' }}>{t('Count')}</th><th>{t('Relationship to this release')}</th><th>{t('Source')}</th><th><span className="sr">{t('Workspace')}</span></th></tr></thead>
              <tbody>
                {rows.map((e) => <EventRow key={e.id} e={e} tz={tz} />)}
              </tbody>
            </table>
          </div>
        )}
        <p className="muted" style={{ margin: 0, fontSize: 12 }}>
          {t('“Direct”, “Ownership” and “Same resource context” say how an Event is linked to this release. An Event is shown because of that link or because it was recorded while this release was current, never because it is thought to explain anything.')}
          {d.release.supersededAt ? ` ${t('Events recorded after a newer revision took over are not attached to this release.')}` : ''}
        </p>
      </section>

      <ScalingContext workloadId={d.workload.uid} tz={tz} windowEntries={data.scaling.length} />
      <p className="muted" style={{ margin: 0, fontSize: 12 }}>{tParts('Events come through {api}, read only. Messages can contain operational detail; they are shown as text and kept to 1 KiB.', { api: <span className="mono">{data.provenance.eventsApi || t('the Kubernetes Events API')}</span> })}</p>
    </div>
  )
}

function EventRow({ e, tz }: { e: ReleaseRecord; tz: string }) {
  const when = e.occurredAt.last ?? e.occurredAt.first
  return (
    <tr>
      <td>
        <b>{e.reason || <span className="muted">{t('no reason reported')}</span>}</b>
        <span className="muted mono" style={{ display: 'block', fontSize: 12 }}>{e.eventType || t('type not reported')}{when ? ` · ${hhmmss(when, tz)}` : ` · ${t('no time reported')}`}</span>
      </td>
      <td>
        <span className="label">{e.resource.kind}</span>
        <span className="mono" style={{ display: 'block', overflowWrap: 'anywhere' }}>{e.resource.name || '—'}</span>
        {e.resource.uid ? <span className="muted mono" style={{ fontSize: 11 }} title={e.resource.uid}>{e.resource.uid.slice(0, 8)}</span> : <span className="muted" style={{ fontSize: 11 }}>{t('no UID reported')}</span>}
        {e.related && <span className="muted" style={{ display: 'block', fontSize: 12 }}>{t('related: {kind} {name}', { kind: e.related.kind, name: e.related.name ?? '' })}</span>}
      </td>
      <td style={{ maxWidth: 360 }}><Message text={e.message} truncated={e.messageTruncated} /></td>
      <td className="mono">{e.count ? <span title={t('from {basis}', { basis: e.count.basis })}>{e.count.value}</span> : <span className="muted" style={{ fontFamily: 'inherit' }}>{t('not reported')}</span>}</td>
      <td><Relation r={e.releaseRelationship} via={e.releaseVia} /></td>
      <td><SourceCell e={e} tz={tz} /></td>
      <td><WorkspaceToggle id={e.recordId} compact /></td>
    </tr>
  )
}

/** What OpsTide has seen of autoscaling for this Deployment. Not a dashboard: operational context. */
export function ScalingContext({ workloadId, tz, windowEntries }: { workloadId: string; tz: string; windowEntries: number }) {
  const { data, error } = useApi<WorkloadScaling>(`/api/v1/workloads/${encodeURIComponent(workloadId)}/scaling`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  return (
    <section aria-label={t('Scaling context')} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <h2 className="label section-title">{t('Scaling context')} <span className="note">· {t('{name}, not part of the release', { name: `${data.workload.namespace}/${data.workload.name}` })}</span></h2>
      <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
        <dt className="label">{t('Replica target')}</dt>
        <dd>{data.replicaTarget === null ? <span className="muted">{t('unknown')}</span> : <span className="mono">{data.replicaTarget}</span>} <span className="muted">{t("as last seen on the Deployment's spec")}</span></dd>
        <dt className="label">{t('Autoscalers')}</dt>
        <dd>{data.hpas.length === 0 ? <span className="muted">{t('No HorizontalPodAutoscaler that names this Deployment has been seen.')}</span> : data.hpas.map((h) => <HPABlock key={h.uid} h={h} tz={tz} />)}</dd>
      </dl>
      <h3 className="label" style={{ margin: 0 }}>{t('Recent scaling entries')} {windowEntries > 0 && <span className="note">· {t('{n} during this release', { n: windowEntries })}</span>}</h3>
      {data.transitions.length === 0 ? <p className="muted" style={{ margin: 0 }}>{t('No replica-target or autoscaler change has been seen.')}</p> : (
        <div className="daygroup" style={{ gap: 0 }}>
          {data.transitions.map((e) => (
            <div key={e.id} className={`entry static ${e.severity}`}>
              <span className="when mono">{hhmmss(e.at, tz)}</span>
              <span className="label">{t('Scaling')}</span>
              <span style={{ minWidth: 0 }}><span>{e.summary}</span><span className="ref mono">{t('observed')}{e.occurredAt ? '' : ` · ${t('the source gives no time')}`}</span></span>
            </div>
          ))}
        </div>
      )}
      {data.records.length > 0 && (
        <details>
          <summary style={{ cursor: 'pointer' }}>{tn(data.records.length, '{n} Kubernetes Event about the autoscaler', '{n} Kubernetes Events about the autoscaler')}</summary>
          <ul style={{ margin: '6px 0 0', paddingLeft: 18 }}>
            {data.records.map((r) => <li key={r.id}>{tParts(r.occurredAt.last ? 'Kubernetes reported {reason} ({type}) at {time}' : 'Kubernetes reported {reason} ({type})', { reason: <b>{r.reason}</b>, type: r.eventType, time: r.occurredAt.last ? hhmmss(r.occurredAt.last, tz) : '' })}: <Message text={r.message} truncated={r.messageTruncated} /></li>)}
          </ul>
        </details>
      )}
    </section>
  )
}

function HPABlock({ h, tz }: { h: HPAView; tz: string }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4, marginBottom: 8 }}>
      <span><span className="mono">{h.namespace}/{h.name}</span>{h.deletedAt && <span className="chip" style={{ marginLeft: 6 }}>{t('deleted')}</span>}</span>
      <span className="mono">{t('current {current} · desired {desired} · min {min} · max {max}', { current: h.currentReplicas, desired: h.desiredReplicas, min: h.minReplicas ?? '—', max: h.maxReplicas })}</span>
      <span className="muted" style={{ fontSize: 12 }}>{tParts('Last scale time reported: {time} · target {target}, linked {relation} ({via})', { time: h.lastScaleTime ? hhmmss(h.lastScaleTime, tz) : t('none'), target: h.target, relation: <Relation r={h.targetRelationship} />, via: h.targetVia })}</span>
      {h.conditions.length > 0 && (
        <ul style={{ margin: 0, paddingLeft: 18 }}>
          {h.conditions.map((c) => <li key={c.type}><span className="mono">{c.type}</span> {c.status}{c.reason ? <> · {c.reason}</> : null}{c.message ? <span className="muted"> · <Message text={c.message} /></span> : null}</li>)}
        </ul>
      )}
      {h.metrics.length > 0 && <span className="muted mono" style={{ fontSize: 12 }}>{h.metrics.map((m) => `${m.name || m.type}: ${t('{a} of {b}', { a: m.current || '?', b: m.target || '?' })}`).join(' · ')}</span>}
    </div>
  )
}
