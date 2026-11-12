import { useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { query } from '../api'
import { Choice } from '../components/Choice'
import { CloseIcon, InfoIcon } from '../components/icons'
import { Note } from '../components/Note'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import {
  beforeWindows, beforeWords, classOf, classWords, explorerLink, groupShort, isFailureLike,
  type BeforeChange, type BeforeWindow, type ExplorerBefore, type ExplorerDetail, type ExplorerState, type Relationship,
} from '../explorer'
import { t, tc, ts } from '../i18n'
import { useApi } from '../live'
import { Link } from '../router'
import { hhmm, hhmmss, isoDate } from '../time'
import { KindMark, ProvChip, resourceText } from './ExplorerTable'

// The drawer of one record: what it is, when it was observed, where the fact comes from, what was recorded before it,
// what it is connected to, and the record as stored. Live state is only ever offered as a labelled link to the
// current view. Tabs, so each question has its own page; nothing that was here before is left out.

const dd = { overflowWrap: 'anywhere' } as const

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="dr-sec">
      <h3 className="label">{title}</h3>
      {children}
    </section>
  )
}

function Rel({ r }: { r: Relationship }) {
  const kind = tc('kind', r.kind)
  const label = r.name ? `${kind} ${r.name}` : r.uid ? `${kind} ${r.uid.length > 14 ? r.uid.slice(0, 8) : r.uid}` : kind
  return (
    <li className="rel">
      <span className="rel-head">
        {r.path ? <Link href={r.path}>{label}</Link> : <span>{label}</span>}
        <span className={`relchip class-${r.classification.toLowerCase()}`}>{classWords[r.classification]}</span>
        {r.resolved === false && <span className="relchip class-unresolved" title={t('OpsTide holds no record of the target')}>{t('not recorded')}</span>}
      </span>
      {r.via && <span className="muted sm">{ts(r.via)}</span>}
      {r.note && <span className="muted sm">{ts(r.note)}</span>}
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
    <div className="dr-stack">
      <p style={{ margin: 0 }}>
        <span className={`chip basis-${data.basis.toLowerCase()}`} style={{ marginLeft: 0 }}>{t(data.basis.replace(/_/g, ' ').toLowerCase())}</span> {ts(data.statement)}
      </p>
      <p className="muted sm" style={{ margin: 0 }}>{t('Coverage then: {note}', { note: ts(data.coverage.note) })}</p>
      {data.recent.length > 0 && (
        <ul style={{ margin: 0, paddingLeft: 18, fontSize: 13 }}>
          {data.recent.slice(0, 5).map((r) => <li key={r.id}><span className="mono">{hhmmss(r.at, tz)}</span> {r.summary}</li>)}
        </ul>
      )}
      <span className="muted sm">{t('This is built only from what was recorded. OpsTide does not fill in a state it did not see.')}</span>
    </div>
  )
}

// How a change was brought in, in the words of this list: the same resource, its owner, config it references, the Node
// it ran on. The relationship class stays in the chip's tooltip.
function howRelated(c: { classification: string; subject: { kind: string } }): string {
  switch (c.classification) {
    case 'DIRECT': return t('same resource')
    case 'OWNERSHIP': return t('its owner')
    case 'REFERENCE': return t('config it references')
    case 'SAME_RESOURCE_CONTEXT': return c.subject.kind === 'Node' ? t('the Node it ran on') : t('same resource context')
  }
  return t('unresolved')
}

function BeforeRow({ c, tz, hrefOf }: { c: BeforeChange; tz: string; hrefOf?: (id: string) => string }) {
  const r = c.record
  const s = c.subject
  return (
    <li className={`bt k-${classOf(r)}`}>
      <i className="bt-dot" aria-hidden="true" />
      <span className="bt-meta">
        <span className="mono">{hhmm(r.at, tz)}</span>
        <span>{beforeWords(c.secondsBefore)}</span>
        <span className={`relchip class-${c.classification.toLowerCase()}`} title={`${classWords[c.classification as keyof typeof classWords] ?? c.classification}${c.via ? ` · ${ts(c.via)}` : ''}`}>{howRelated(c)}</span>
      </span>
      <span className="bt-title">{hrefOf ? <Link href={hrefOf(r.id)}>{r.summary}</Link> : r.summary}</span>
      <span className="bt-sub">
        {s.kind} <span className="mono">{s.namespace ? `${s.namespace}/` : ''}{s.name ?? ''}</span>
        {c.writer ? <> · {t('written by {writer}', { writer: c.writer })}</> : null}
        <ProvChip p={r.provenance} />
      </span>
    </li>
  )
}

const beforeEmptyWords: Record<NonNullable<ExplorerBefore['empty']>['kind'], string> = {
  get no_changes_recorded() { return t('No changes recorded in this window, and OpsTide was watching all of what they come from throughout.') },
  get no_changes_recorded_with_gaps() { return t('No changes recorded, but some of the watches they come from had gaps in this window: a change there may not have been recorded.') },
  get unresolved() { return t('No changes recorded, but OpsTide could not resolve everything related to this record, so the changes of what it could not resolve were not looked for.') },
  get outside_retention() { return t('No changes recorded, but part or all of this window is older than the records OpsTide still holds: a change then may have been removed.') },
  get before_first_observation() { return t('No changes recorded. This window reaches back before OpsTide first observed the cluster, so what happened then is not known.') },
}

const unresolvedWords: Record<ExplorerBefore['coverage']['unresolved'][number], string> = {
  get unresolved_subject() { return t('Some related resources could not be resolved, so their changes were not looked for.') },
  get nothing_watched() { return t('Nothing related to this record names a watch its changes could come from.') },
  get subjects_capped() { return t('Only the first {n} related resources were looked at.', { n: 100 }) },
}

const windowWords: Record<BeforeWindow, () => string> = {
  '15m': () => t('15 min'), '1h': () => t('1 h'), '6h': () => t('6 h'), '24h': () => t('24 h'),
}

// "Recorded before this": the changes OpsTide recorded in a window before a failure-like record, on its resource and on
// the resources related to it, with the window's coverage. A list of earlier records, never a cause.
function Before({ at, win, setWin, state, tz, hrefOf }: {
  at: string
  win: BeforeWindow
  setWin: (w: BeforeWindow) => void
  state: { data?: ExplorerBefore; error?: Error; loading: boolean }
  tz: string
  hrefOf?: (id: string) => string
}) {
  const { data, error, loading } = state
  // The list is the answer's own window: while another window loads, the old list stays and says which it is.
  const stale = loading || (data !== undefined && data.window !== win)
  const missing = data?.coverage.scopes.filter((sc) => sc.state !== 'watching') ?? []
  const heldFrom = data?.coverage.heldFrom
  return (
    <div className="dr-stack" aria-busy={stale}>
      <div className="bt-head">
        <span>{t('Before {time}', { time: hhmm(at, tz) })}</span>
        <Choice label={t('Window before the record')} value={win} onChange={setWin} size="xs" className="mono"
          options={beforeWindows.map((w) => ({ value: w, name: windowWords[w](), full: t('The {window} before the record', { window: windowWords[w]() }) }))} />
      </div>
      {stale && data && <span className="muted sm" role="status">{t('Loading the {window} window…', { window: windowWords[win]() })}</span>}
      {error && <p className="error" role="alert" style={{ margin: 0 }}>{data ? t('Could not refresh this list ({error}); what is shown is an earlier answer.', { error: error.message }) : error.message}</p>}
      {!data ? (!error && <p className="muted" style={{ margin: 0 }}>{t('Loading…')}</p>) : (
        <>
          {data.changes.length > 0 ? (
            <ol className="bt-list" style={{ opacity: stale ? 0.6 : 1 }} aria-label={t('Changes, newest first')}>
              {data.changes.map((c) => <BeforeRow key={c.record.id} c={c} tz={tz} hrefOf={hrefOf} />)}
            </ol>
          ) : (
            <p className="bt-empty">{beforeEmptyWords[data.empty?.kind ?? 'no_changes_recorded_with_gaps']}</p>
          )}
          {data.truncated && <span className="muted sm">{t('Only the newest {n} are listed.', { n: data.changes.length })}</span>}
          {data.maintenance
            ? <span className="sm"><span className="chip maint" style={{ marginLeft: 0 }}>{t('maintenance: {name}', { name: data.maintenance.name })}</span> {t('This record falls inside a maintenance window declared in the configuration ({from}–{to}). That is context, not a cause.', { from: hhmmss(data.maintenance.from, tz), to: hhmmss(data.maintenance.to, tz) })}</span>
            : (data.maintenanceWindows?.length ?? 0) > 0 && <span className="muted sm">{t('A maintenance window was open during part of this window: {list}. This record falls outside it.', { list: data.maintenanceWindows!.map((m) => `${m.name} ${hhmm(m.from, tz)}–${hhmm(m.to, tz)}`).join(', ') })}</span>}
          {data.maintenanceWindowsTruncated && <span className="muted sm">{t('More maintenance window occurrences overlap this window than are listed; the list shows only the first.')}</span>}
          <div className={`callout${data.coverage.state === 'watching' ? '' : ' gap'}`}>
            <InfoIcon size={16} />
            <div>
              <span>
                {data.coverage.state === 'watching'
                  ? t('Coverage: OpsTide was watching all of what these changes come from throughout the window.')
                  : missing.length > 0
                    ? t('Coverage: not watched throughout the window: {list}.', { list: missing.map((sc) => `${t(sc.label)}${sc.namespace ? ` (${sc.namespace})` : ''} (${t(sc.state.replace(/_/g, ' '))})`).join(', ') })
                    : t('Coverage: not complete for this window.')}
                {data.changes.length > 0 && data.coverage.state === 'before_first_observation' ? ` ${t('Part of the window is before OpsTide first observed the cluster.')}` : ''}
                {heldFrom ? ` ${t('Records from before {time} are no longer held (age limit or size budget).', { time: hhmmss(heldFrom, tz) })}` : ''}
                {data.coverage.unresolved.map((u) => ` ${unresolvedWords[u]}`).join('')}
              </span>
              <Note block more={t('These changes were recorded before this record, on it or on resources connected to it. Being earlier does not make any of them a cause: OpsTide does not say whether any of them had anything to do with it.')}>{t('Before, not because')}</Note>
            </div>
          </div>
          {data.related.length > 0 && (
            <span className="muted sm">
              {t('Looked at:')} {data.related.map((x, i) => (
                <span key={i} title={ts(x.via)}>{i > 0 ? ', ' : ''}{x.kind}{x.name ? <span className="mono"> {x.name}</span> : null} ({howRelated({ classification: x.classification, subject: x })})</span>
              ))}
            </span>
          )}
        </>
      )}
    </div>
  )
}

type Tab = 'summary' | 'before' | 'relationships' | 'raw'

export function RecordPanel({ id, tz, close, hrefOf }: { id: string; tz: string; close: () => void; hrefOf?: (id: string) => string }) {
  const { data: d, error } = useApi<ExplorerDetail>(`/api/v1/explorer/records/${encodeURIComponent(id)}`)
  const failure = !!d && isFailureLike(d.record)
  const [win, setWin] = useState<BeforeWindow>('1h')
  const before = useApi<ExplorerBefore>(failure ? `/api/v1/explorer/records/${encodeURIComponent(id)}/before${query({ window: win })}` : null, { keep: true })
  const [picked, setPicked] = useState<Tab | null>(null)
  const tabsRef = useRef<HTMLDivElement>(null)

  if (error && !d) {
    return (
      <div className="dr-body">
        <p className="error" role="alert">{error.message === 'not found' ? t('This record is no longer held (removed by retention) or never existed.') : error.message}</p>
        <button type="button" className="button" onClick={close}>{t('Close')}</button>
      </div>
    )
  }
  if (!d) return <div className="dr-body muted" aria-busy="true">{t('Loading…')}</div>
  const r = d.record
  const o = d.observation
  const f = d.original.entry?.facts ?? {}
  const ev = d.original.event
  const res = r.resource

  const tabs: { key: Tab; label: string; badge?: number; quiet?: boolean }[] = [
    { key: 'summary', label: t('Summary') },
    ...(failure ? [{ key: 'before' as Tab, label: t('Before'), badge: before.data?.changes.length }] : []),
    { key: 'relationships', label: t('Relationships'), badge: d.relationships.length, quiet: true },
    { key: 'raw', label: t('Raw') },
  ]
  const tab: Tab = picked && tabs.some((x) => x.key === picked) ? picked : failure ? 'before' : 'summary'
  const onTabKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const i = tabs.findIndex((x) => x.key === tab)
    let n = -1
    if (e.key === 'ArrowRight') n = (i + 1) % tabs.length
    else if (e.key === 'ArrowLeft') n = (i - 1 + tabs.length) % tabs.length
    else if (e.key === 'Home') n = 0
    else if (e.key === 'End') n = tabs.length - 1
    if (n < 0) return
    e.preventDefault()
    setPicked(tabs[n].key)
    tabsRef.current?.querySelectorAll<HTMLButtonElement>('[role=tab]')[n]?.focus()
  }

  const kindLine = [res.kind || groupShort[r.group], res.namespace].filter(Boolean).join(' · ')

  return (
    <>
      <div className="dr-head">
        <KindMark r={r} size={20} />
        <div className="dr-title">
          <span className="dr-kind">{kindLine} <span className="mono">· {r.type}</span></span>
          <h2 className="dr-name" tabIndex={-1} id="record-title">{r.summary}</h2>
        </div>
        <WorkspaceToggle id={r.id} icon />
        <button type="button" className="ibtn small" data-tip={t('Close')} aria-label={t('Close the details')} onClick={close}><CloseIcon size={16} /></button>
      </div>
      <div className="dr-tabs" role="tablist" aria-label={t('Record')} ref={tabsRef} onKeyDown={onTabKey}>
        {tabs.map((x) => (
          <button key={x.key} type="button" role="tab" id={`dr-tab-${x.key}`} aria-controls={`dr-panel-${x.key}`} aria-selected={tab === x.key} tabIndex={tab === x.key ? 0 : -1}
            onClick={() => setPicked(x.key)}>
            {x.label}
            {x.badge !== undefined && x.badge > 0 && <span className={`tab-badge mono${x.quiet ? ' quiet' : ''}`}>{x.badge}</span>}
          </button>
        ))}
      </div>

      {tab === 'summary' && (
        <section role="tabpanel" id="dr-panel-summary" aria-labelledby="dr-tab-summary" className="dr-body">
          <div className="dr-facts-top">
            <ProvChip p={d.provenance.class} title={d.provenance.source} />
            {r.severity && r.severity !== 'info' && <span className={`sev k-${classOf(r)}`}>{t(r.severity)}</span>}
            {r.maintenance && <span className="chip maint" style={{ marginLeft: 0 }}>{t('maintenance: {name}', { name: r.maintenance.name })}</span>}
            <span className="mono muted sm">{isoDate(r.at, tz)} {hhmmss(r.at, tz)}</span>
          </div>
          {res.name && (
            <p className="dr-res">
              <span className="mono">{res.kind} {resourceText(r)}</span>
              {res.uid && <> · <Link href={explorerLink({ uid: res.uid, range: '7d' })} title={t('Everything recorded about this exact resource (by UID)')}>{t('This resource in Explorer')}</Link></>}
            </p>
          )}

          {r.maintenance && (
            <Section title={t('Maintenance window')}>
              <p className="muted sm" style={{ margin: 0 }}>
                <ProvChip p={r.maintenance.provenance} />{' '}
                {t('Inside maintenance window {name} ({from}–{to}), as declared in the configuration. The record is shown as recorded; the window is context, not a cause.', { name: r.maintenance.name, from: hhmmss(r.maintenance.from, tz), to: hhmmss(r.maintenance.to, tz) })}
                {r.maintenance.note ? ` ${r.maintenance.note}` : ''}
              </p>
            </Section>
          )}

          <Section title={t('Observation')}>
            <dl className="facts">
              <dt className="label">{t('Record time')}</dt>
              <dd className="mono">{hhmmss(o.recordTime, tz)} <span className="muted" style={{ fontFamily: 'inherit' }}>· {ts(o.explanation)}</span></dd>
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
              {(f.keysAdded || f.keysRemoved || f.keysChanged || f.contentChanged || f.managesData || f.updatesFolded || f.alsoWrittenBy) && (
                <dl className="facts">
                  {f.contentChanged && <><dt className="label">{t('Content changed')}</dt><dd>{ts(f.contentChanged)}</dd></>}
                  {f.managesData && <><dt className="label">{t('Owns data')}</dt><dd>{ts(f.managesData)}</dd></>}
                  {f.updatesFolded && <><dt className="label">{t('Updates folded')}</dt><dd className="mono">{f.updatesFolded}</dd></>}
                  {f.alsoWrittenBy && <><dt className="label">{t('Also written by')}</dt><dd className="mono" style={dd}>{f.alsoWrittenBy}</dd></>}
                  {f.keysChanged && <><dt className="label">{t('Keys changed')}</dt><dd className="mono" style={dd}>{f.keysChanged}</dd></>}
                  {f.keysAdded && <><dt className="label">{t('Keys added')}</dt><dd className="mono" style={dd}>{f.keysAdded}</dd></>}
                  {f.keysRemoved && <><dt className="label">{t('Keys removed')}</dt><dd className="mono" style={dd}>{f.keysRemoved}</dd></>}
                </dl>
              )}
              {/^(configmap|secret)\./.test(r.type)
                ? <Note more={t('A field manager is the name a client gave itself (argocd-controller, kubectl-edit, helm): it says which software wrote, not which person. Its time moves on any write it makes, so a write says who wrote, not what changed; for a ConfigMap, what changed is told by comparing its content with the previous observation. OpsTide keeps the name, the kind of operation and the time; for a ConfigMap also the names of its keys and a keyed hash of its content, never a value. A Secret is read as metadata only: its data is never requested; annotations that can contain it (last-applied-configuration) are dropped as they arrive, and its key names are never kept.')}>{t('A client name, not a person')}</Note>
                : <Note more={t('A field manager is the name a client gave itself (argocd-controller, kubectl-edit, helm): it says which software wrote, not which person; the API server\'s audit log holds who. Kubernetes moves a manager\'s time whenever it writes any field it owns, so this says a manager of the spec wrote, not that the spec changed. Only the name, the kind of operation and the time are kept, never the fields or their values.')}>{t('A client name, not a person')}</Note>}
            </Section>
          )}

          <Section title={t('Provenance')}>
            <p style={{ margin: 0 }}><ProvChip p={d.provenance.class} /> <span className="mono">{d.provenance.source}</span></p>
            <p className="muted sm" style={{ margin: 0 }}>{ts(d.provenance.explanation)}</p>
          </Section>

          {d.derivation && (
            <Section title={t('How this was derived')}>
              <dl className="facts">
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
              {d.derivation.note && <p className="muted sm" style={{ margin: 0 }}>{ts(d.derivation.note)}</p>}
            </Section>
          )}

          <Section title={t('Coverage at this time')}>
            <p style={{ margin: 0 }}><span className={`chip cov-${d.coverage.state}`} style={{ marginLeft: 0 }}>{t(d.coverage.state.replace(/_/g, ' '))}</span> <span className="muted">{ts(d.coverage.note)}</span></p>
          </Section>

          {res.uid && ['Node', 'StatefulSet', 'DaemonSet', 'Deployment', 'HorizontalPodAutoscaler', 'Pod', 'Job', 'CronJob'].includes(res.kind) && (
            <Section title={t('State at this time')}>
              <StateAt key={r.id} uid={res.uid} kind={res.kind} ns={res.namespace} at={r.at} tz={tz} />
            </Section>
          )}

          {d.limitations.length > 0 && (
            <Section title={t('Limits of this record')}>
              <ul style={{ margin: 0, paddingLeft: 18, display: 'flex', flexDirection: 'column', gap: 4, fontSize: 13 }}>{d.limitations.map((l, i) => <li key={i}>{ts(l)}</li>)}</ul>
            </Section>
          )}

          {d.current && (
            <p className="sm" style={{ margin: 0 }}>
              <Link href={d.current.path}>{t('Open the current view')}</Link> <span className="muted">· {ts(d.current.label)}</span>
            </p>
          )}
        </section>
      )}

      {tab === 'before' && (
        <section role="tabpanel" id="dr-panel-before" aria-label={t('Recorded before this')} className="dr-body">
          <Before at={r.at} win={win} setWin={setWin} state={before} tz={tz} hrefOf={hrefOf} />
        </section>
      )}

      {tab === 'relationships' && (
        <section role="tabpanel" id="dr-panel-relationships" aria-labelledby="dr-tab-relationships" className="dr-body">
          {d.relationships.length === 0 ? <p className="muted" style={{ margin: 0 }}>{t('This record names no other resource.')}</p> : (
            <ul className="rels">{d.relationships.map((x, i) => <Rel key={i} r={x} />)}</ul>
          )}
          <Note block more={t('A relationship says how two records are connected. It does not say one caused the other.')}>{t('Connection, not cause')}</Note>
        </section>
      )}

      {tab === 'raw' && (
        <section role="tabpanel" id="dr-panel-raw" aria-labelledby="dr-tab-raw" className="dr-body">
          <Section title={t('The record as stored')}>
            {ev ? (
              <dl className="facts raw">
                <dt className="label">{t('Reason')}</dt><dd className="mono">{r.reason} <span className="muted" style={{ fontFamily: 'inherit' }}>· {r.eventType}</span></dd>
                <dt className="label">{t('Message')}</dt><dd style={{ whiteSpace: 'pre-wrap', ...dd }}>{r.message || <span className="muted">{t('none reported')}</span>}{r.messageTruncated ? <span className="muted"> … {t('(cut by OpsTide)')}</span> : null}</dd>
                {ev.reporter && <><dt className="label">{t('Reported by')}</dt><dd className="mono">{ev.reporter}</dd></>}
                {ev.action && <><dt className="label">{t('Action')}</dt><dd className="mono">{ev.action}</dd></>}
                <dt className="label">Event API</dt><dd className="mono">{ev.source}</dd>
                <dt className="label">Event UID</dt><dd className="mono" style={dd}>{ev.sourceUid}</dd>
              </dl>
            ) : (
              <dl className="facts raw">
                <dt className="label">{t('Type')}</dt><dd className="mono">{r.type}</dd>
                {Object.keys(f).length === 0 ? <><dt className="label">{t('Fields')}</dt><dd className="muted">{t('none beyond the summary')}</dd></> :
                  Object.entries(f).sort(([a], [b]) => a.localeCompare(b)).map(([k, v]) => <div key={k} style={{ display: 'contents' }}><dt className="mono">{k}</dt><dd className="mono" style={{ whiteSpace: 'pre-wrap', ...dd }}>{v}</dd></div>)}
              </dl>
            )}
            <span className="mono muted" style={{ fontSize: 11 }}>id {r.id}{res.uid ? ` · uid ${res.uid}` : ''}</span>
          </Section>
        </section>
      )}
    </>
  )
}
