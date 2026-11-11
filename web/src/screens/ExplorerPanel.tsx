import { useState } from 'react'
import { query } from '../api'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import { classWords, explorerLink, provWord, typeWord, type ExplorerDetail, type ExplorerState, type Relationship } from '../explorer'
import { t } from '../i18n'
import { useApi } from '../live'
import { Link } from '../router'
import { hhmmss } from '../time'
import { Note } from '../components/Note'

// The drilldown of one record: what it is, when it was observed, where the fact comes from, what it is connected to,
// and the record as stored. Live state is only ever offered as a labelled link to the current view.

const dd = { overflowWrap: 'anywhere' } as const

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section aria-label={title} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <h3 className="label section-title" style={{ fontSize: 11 }}>{title}</h3>
      {children}
    </section>
  )
}

function Rel({ r }: { r: Relationship }) {
  const label = r.name ? `${r.kind} ${r.name}` : r.uid ? `${r.kind} ${r.uid.length > 14 ? r.uid.slice(0, 8) : r.uid}` : r.kind
  return (
    <li style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <span>
        {r.path ? <Link href={r.path}>{label}</Link> : <span>{label}</span>}
        <span className={`chip class-${r.classification.toLowerCase()}`}>{classWords[r.classification]}</span>
        {r.resolved === false && <span className="chip" title={t('OpsTide holds no record of the target')}>{t('not recorded')}</span>}
      </span>
      {r.via && <span className="muted" style={{ fontSize: 12 }}>{r.via}</span>}
      {r.note && <span className="muted" style={{ fontSize: 12 }}>{r.note}</span>}
    </li>
  )
}

function StateAt({ uid, kind, ns, at, tz }: { uid: string; kind: string; ns?: string; at: string; tz: string }) {
  const [ask, setAsk] = useState(false)
  const path = ask ? `/api/v1/explorer/state${query({ uid, kind, namespace: ns, at })}` : null
  const { data, error } = useApi<ExplorerState>(path)
  if (!ask) {
    return <button type="button" className="button" style={{ alignSelf: 'flex-start' }} onClick={() => setAsk(true)}>{t('What did OpsTide know of this resource at this time?')}</button>
  }
  if (error) return <p className="error" role="alert">{error.message}</p>
  if (!data) return <p className="muted">{t('Loading…')}</p>
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <p style={{ margin: 0 }}>
        <span className={`chip basis-${data.basis.toLowerCase()}`}>{t(data.basis.replace(/_/g, ' ').toLowerCase())}</span> {data.statement}
      </p>
      <p className="muted" style={{ margin: 0, fontSize: 12 }}>{t('Coverage then: {note}', { note: data.coverage.note })}</p>
      {data.recent.length > 0 && (
        <ul style={{ margin: 0, paddingLeft: 18, fontSize: 13 }}>
          {data.recent.slice(0, 5).map((r) => <li key={r.id}><span className="mono">{hhmmss(r.at, tz)}</span> {r.summary}</li>)}
        </ul>
      )}
      <span className="muted" style={{ fontSize: 12 }}>{t('This is built only from what was recorded. OpsTide does not fill in a state it did not see.')}</span>
    </div>
  )
}

export function RecordPanel({ id, tz, close }: { id: string; tz: string; close: () => void }) {
  const { data: d, error } = useApi<ExplorerDetail>(`/api/v1/explorer/records/${encodeURIComponent(id)}`)
  if (error && !d) {
    return (
      <div>
        <p className="error" role="alert">{error.message === 'not found' ? t('This record is no longer held (removed by retention) or never existed.') : error.message}</p>
        <button type="button" className="button" onClick={close}>{t('Close')}</button>
      </div>
    )
  }
  if (!d) return <div className="empty" aria-busy="true">{t('Loading…')}</div>
  const r = d.record
  const o = d.observation
  const f = d.original.entry?.facts ?? {}
  const ev = d.original.event
  const res = r.resource
  return (
    <>
      <div className="panel-head">
        <div style={{ display: 'flex', gap: 8, alignItems: 'baseline' }}>
          <span className="label">{typeWord(r.type)}</span>
          <span className="grow" style={{ flex: 1 }} />
          <button type="button" className="ws-toggle" onClick={close} aria-label={t('Close the details')}>✕</button>
        </div>
        <span className="name" tabIndex={-1} id="record-title">{r.summary}</span>
        {res.name && <span className="mono muted" style={{ fontSize: 12 }}>{res.kind} {res.namespace ? `${res.namespace}/` : ''}{res.name}</span>}
        <span style={{ display: 'flex', gap: 8, flexWrap: 'wrap', paddingTop: 4 }}>
          <WorkspaceToggle id={r.id} />
          {res.uid && <Link className="button" href={explorerLink({ uid: res.uid, range: '7d' })} title={t('Everything recorded about this exact resource (by UID)')}>{t('This resource in Explorer')}</Link>}
        </span>
      </div>

      <Section title={t('Observation')}>
        <dl className="facts" style={{ gridTemplateColumns: '130px minmax(0, 1fr)' }}>
          <dt className="label">{t('Record time')}</dt>
          <dd className="mono">{hhmmss(o.recordTime, tz)} <span className="muted" style={{ fontFamily: 'inherit' }}>· {o.explanation}</span></dd>
          <dt className="label">{t('Occurred')}</dt>
          <dd className="mono">{o.occurredAt ? <>{hhmmss(o.occurredAt, tz)}<span className="muted"> {hhmmss(o.occurredAt, 'UTC')} UTC</span></> : <span className="muted" style={{ fontFamily: 'inherit' }}>{t('the source gives no time')}</span>}</dd>
          {o.lastOccurredAt && <><dt className="label">{t('Last occurred')}</dt><dd className="mono">{hhmmss(o.lastOccurredAt, tz)}</dd></>}
          <dt className="label">{t('Observed')}</dt><dd className="mono">{hhmmss(o.observedAt, tz)} <span className="muted" style={{ fontFamily: 'inherit' }}>· {t('when OpsTide first saw it')}</span></dd>
          {o.recordedAt && <><dt className="label">{t('Recorded')}</dt><dd className="mono">{hhmmss(o.recordedAt, tz)}</dd></>}
          {o.origin && <><dt className="label">{t('Origin')}</dt><dd>{o.origin === 'adopted' ? t('already present when OpsTide began watching') : t('seen being created')}</dd></>}
          {r.count && <><dt className="label">{t('Count')}</dt><dd className="mono">{r.count.value} <span className="muted" style={{ fontFamily: 'inherit' }}>({r.count.basis})</span></dd></>}
        </dl>
      </Section>

      {f.fieldManager && (
        <Section title={t('Written by')}>
          <p style={{ margin: 0 }}><span className="mono">{f.fieldManager}</span> <span className="muted">· {f.operation}{f.subresource ? `, ${f.subresource}` : ''}{f.writtenAt && !Number.isNaN(Date.parse(f.writtenAt)) ? ` · ${hhmmss(f.writtenAt, tz)}` : ''}</span></p>
          <Note more={t('A field manager is the name a client gave itself (argocd-controller, kubectl-edit, helm): it says which software wrote, not which person; the API server\'s audit log holds who. Kubernetes moves a manager\'s time whenever it writes any field it owns, so this says a manager of the spec wrote, not that the spec changed. Only the name, the kind of operation and the time are kept, never the fields or their values.')}>{t('A client name, not a person')}</Note>
        </Section>
      )}

      <Section title={t('Provenance')}>
        <p style={{ margin: 0 }}><span className={`chip prov-${d.provenance.class.toLowerCase()}`}>{provWord(d.provenance.class)}</span> <span className="mono">{d.provenance.source}</span></p>
        <p className="muted" style={{ margin: 0, fontSize: 13 }}>{d.provenance.explanation}</p>
      </Section>

      {d.derivation && (
        <Section title={t('How this was derived')}>
          <dl className="facts" style={{ gridTemplateColumns: '110px minmax(0, 1fr)' }}>
            <dt className="label">{t('Rule')}</dt><dd className="mono" style={dd}>{d.derivation.ruleId}</dd>
            <dt className="label">{t('Rule set')}</dt><dd className="mono">{d.derivation.ruleSet} <span className="muted" style={{ fontFamily: 'inherit' }}>· {t('version')}</span> {d.derivation.ruleVersion}</dd>
            {Object.keys(d.derivation.inputs).length > 0 && (
              <><dt className="label">{t('Inputs')}</dt><dd className="mono" style={dd}>{Object.entries(d.derivation.inputs).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => <span key={k} style={{ display: 'block' }}>{k} = {v}</span>)}</dd></>
            )}
            <dt className="label">{t('Evidence')}</dt>
            <dd>{d.derivation.evidenceRefs.length === 0 ? <span className="muted">{t('none recorded')}</span> : d.derivation.evidenceRefs.map((x) => (
              <span key={`${x.kind}${x.id}`} style={{ display: 'block' }}>{x.path ? <Link href={x.path}>{x.kind}</Link> : x.kind} <span className="mono muted">{x.id.length > 14 ? x.id.slice(0, 8) : x.id}</span></span>
            ))}</dd>
          </dl>
          {d.derivation.note && <p className="muted" style={{ margin: 0, fontSize: 12 }}>{d.derivation.note}</p>}
        </Section>
      )}

      <Section title={t('Relationships')}>
        {d.relationships.length === 0 ? <p className="muted" style={{ margin: 0 }}>{t('This record names no other resource.')}</p> : (
          <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 8 }}>{d.relationships.map((x, i) => <Rel key={i} r={x} />)}</ul>
        )}
        <Note more={t('A relationship says how two records are connected. It does not say one caused the other.')}>{t('Connection, not cause')}</Note>
      </Section>

      <Section title={t('The record as stored')}>
        {ev ? (
          <dl className="facts" style={{ gridTemplateColumns: '110px minmax(0, 1fr)' }}>
            <dt className="label">{t('Reason')}</dt><dd className="mono">{r.reason} <span className="muted" style={{ fontFamily: 'inherit' }}>· {r.eventType}</span></dd>
            <dt className="label">{t('Message')}</dt><dd style={{ whiteSpace: 'pre-wrap', ...dd }}>{r.message || <span className="muted">{t('none reported')}</span>}{r.messageTruncated ? <span className="muted"> … {t('(cut by OpsTide)')}</span> : null}</dd>
            {ev.reporter && <><dt className="label">{t('Reported by')}</dt><dd className="mono">{ev.reporter}</dd></>}
            {ev.action && <><dt className="label">{t('Action')}</dt><dd className="mono">{ev.action}</dd></>}
            <dt className="label">Event API</dt><dd className="mono">{ev.source}</dd>
            <dt className="label">Event UID</dt><dd className="mono" style={dd}>{ev.sourceUid}</dd>
          </dl>
        ) : (
          <dl className="facts" style={{ gridTemplateColumns: '110px minmax(0, 1fr)' }}>
            <dt className="label">{t('Type')}</dt><dd className="mono">{r.type}</dd>
            {Object.keys(f).length === 0 ? <><dt className="label">{t('Fields')}</dt><dd className="muted">{t('none beyond the summary')}</dd></> :
              Object.entries(f).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => <div key={k} style={{ display: 'contents' }}><dt className="label">{k}</dt><dd className="mono" style={{ whiteSpace: 'pre-wrap', ...dd }}>{v}</dd></div>)}
          </dl>
        )}
        <span className="mono muted" style={{ fontSize: 11 }}>id {r.id}{res.uid ? ` · uid ${res.uid}` : ''}</span>
      </Section>

      <Section title={t('Coverage at this time')}>
        <p style={{ margin: 0 }}><span className={`chip cov-${d.coverage.state}`}>{t(d.coverage.state.replace(/_/g, ' '))}</span> <span className="muted">{d.coverage.note}</span></p>
      </Section>

      {res.uid && ['Node', 'StatefulSet', 'DaemonSet', 'Deployment', 'HorizontalPodAutoscaler', 'Pod', 'Job', 'CronJob'].includes(res.kind) && (
        <Section title={t('State at this time')}>
          <StateAt key={r.id} uid={res.uid} kind={res.kind} ns={res.namespace} at={r.at} tz={tz} />
        </Section>
      )}

      {d.limitations.length > 0 && (
        <Section title={t('Limits of this record')}>
          <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4, fontSize: 13 }}>{d.limitations.map((l, i) => <li key={i}>{l}</li>)}</ul>
        </Section>
      )}

      {d.current && (
        <p style={{ margin: 0, fontSize: 13 }}>
          <Link href={d.current.path}>{t('Open the current view')}</Link> <span className="muted">· {d.current.label}</span>
        </p>
      )}
    </>
  )
}
