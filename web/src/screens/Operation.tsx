import { Fragment, type ReactNode } from 'react'
import { query, type OperationDetail, type RunDetail, type RunEvidence } from '../api'
import { Failed, Loading } from '../components/Shell'
import { WorkspaceToggle } from '../components/WorkspaceToggle'
import { explorerLink, rangeAround, recordIdOf } from '../explorer'
import { t } from '../i18n'
import { Legend, Timeline } from '../components/Timeline'
import { useApi } from '../live'
import { Link, navigate, withParams } from '../router'
import { duration, hhmm, hhmmss, isoDate, relativeTime, shortDay } from '../time'
import { statusWord } from './Schedules'

/** t() for a sentence with elements in it: the placeholders are filled with the given parts, in the translation's order. */
function rich(text: string, parts: Record<string, ReactNode>): ReactNode {
  return t(text).split(/(\{\w+\})/).map((piece, i) => {
    const m = /^\{(\w+)\}$/.exec(piece)
    return m && m[1] in parts ? <Fragment key={i}>{parts[m[1]]}</Fragment> : piece
  })
}

export function Operation({ id, params, tz }: { id: string; params: URLSearchParams; tz: string }) {
  const before = params.get('before') ?? ''
  const { data, error } = useApi<OperationDetail>(`/api/v1/operations/${encodeURIComponent(id)}${query({ tz, before, runs: 20 })}`)
  if (error && !data) return <div className="page"><Failed error={error} /></div>
  if (!data) return <div className="page"><Loading /></div>

  const op = data.operation
  const runId = params.get('run') ?? data.runs.find((r) => r.status === 'failed')?.id ?? data.runs[0]?.id
  const last = data.runs[0]
  const perDay = data.runs.length ? data.runs : []
  const expected = op.expectedSeconds ? duration(op.expectedSeconds) : ''
  const runsPerDay = data.timesToday
  const today = isoDate(data.now, tz)

  return (
    <div className="page" style={{ paddingTop: 24 }}>
      <div className="head-row" style={{ gap: '12px 28px' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
          <Link href="/schedules" style={{ fontSize: 13, textDecoration: 'none' }}>‹ {t('Schedules')}</Link>
          <h1 className="title">{op.displayName || op.name}</h1>
          <span className="mono muted" style={{ fontSize: 13 }}>{op.namespace}/{op.name} · CronJob · uid {op.id.slice(0, 4)}…{op.id.slice(-4)}</span>
        </div>
        <span style={{ display: 'flex', flexDirection: 'column', gap: 2, paddingBottom: 4 }}>
          <span className="label">{t('Last run')}</span>
          <span>
            {last ? (
              <>
                <b className={last.status === 'failed' ? 'fail' : undefined} style={{ fontWeight: 600 }}>{statusWord(last.status)}</b>{' '}
                <span className="mono">{hhmm(last.finishedAt ?? last.createdAt, tz)}</span>
              </>
            ) : (
              <span className="muted">{t('none seen')}</span>
            )}
            <span className="muted"> · {op.deletedAt ? t('CronJob deleted') : data.next ? rich('next expected {when}', { when: <span className="mono">{relativeTime(data.next, data.now, tz)}</span> }) : op.suspended ? t('suspended') : t('no next run')}</span>
          </span>
        </span>
        <span className="grow" />
        <Link className="button" style={{ display: 'inline-flex', alignItems: 'center' }} href={explorerLink({ operation: op.id, range: '7d' })}>{t('Records in Explorer')}</Link>
        {data.clustrailUrl && <a className="button" style={{ display: 'inline-flex', alignItems: 'center', textDecoration: 'none', color: 'inherit' }} href={data.clustrailUrl} rel="noopener noreferrer" title={t('Opens this CronJob in Clustrail, if it is installed on this computer')}>{t('Open in Clustrail')}</a>}
      </div>

      <div className="two">
        <section aria-label={t('What it does')} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <h2 className="label section-title">{t('What it does')}</h2>
          {op.description ? (
            <p style={{ margin: 0, fontSize: 16, lineHeight: 1.5, whiteSpace: 'pre-line' }}>{op.description}</p>
          ) : (
            <p className="muted" style={{ margin: 0 }}>{rich('Not described. Add {annotation} to the CronJob in its chart.', { annotation: <span className="mono">opstide.mihnk.org/description</span> })}</p>
          )}
          <dl className="facts">
            <dt className="label">{t('Owner')}</dt><dd>{op.owner || <span className="muted">{t('not set')}</span>}</dd>
            <dt className="label">{t('Criticality')}</dt><dd style={{ textTransform: 'capitalize' }}>{op.criticality || <span className="muted" style={{ textTransform: 'none' }}>{t('not set')}</span>}</dd>
            <dt className="label">{t('Runbook')}</dt>
            <dd>{op.runbookUrl ? <a href={op.runbookUrl} target="_blank" rel="noopener noreferrer">{op.runbookUrl.replace(/^https:\/\//, '')}</a> : <span className="muted">{t('not set')}</span>}</dd>
          </dl>
          {!!op.metadataProblems?.length && <span className="fail" style={{ fontSize: 12 }}>{op.metadataProblems.join('. ')}.</span>}
          <span className="muted" style={{ fontSize: 12 }}>{rich("From the CronJob's {annotations} annotations. Edit them in the chart; OpsTide only reads them.", { annotations: <span className="mono">opstide.mihnk.org/*</span> })}</span>
        </section>

        <section aria-label={t('What is expected')} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <h2 className="label section-title">{t('What is expected')}</h2>
          <p style={{ margin: 0, fontSize: 16, lineHeight: 1.5 }}>
            {op.scheduleError ? t('The schedule cannot be read: {error}', { error: op.scheduleError }) : expected ? (
              rich(
                runsPerDay ? (runsPerDay === 1 ? '{n} expected time today, each done in {limit}.' : '{n} expected times today, each done in {limit}.') : 'Nothing expected today, each done in {limit}.',
                { n: runsPerDay, limit: <b>{t('up to {d}', { d: expected })}</b> },
              )
            ) : runsPerDay ? (
              t(runsPerDay === 1 ? '{n} expected time today.' : '{n} expected times today.', { n: runsPerDay })
            ) : t('Nothing expected today.')}
          </p>
          <dl className="facts">
            <dt className="label">{t('Schedule')}</dt>
            <dd className="mono">{op.schedule} <span className="muted">{op.timeZone || t("{zone} (no timeZone set; the controller's zone)", { zone: op.zoneAssumed ?? '' })}</span></dd>
            <dt className="label">{t('Overlap')}</dt>
            <dd><span className="mono">{op.concurrency}</span> <span className="muted">· {op.concurrency === 'Forbid' ? t('a run is skipped while the previous one is active') : op.concurrency === 'Replace' ? t('a new run replaces one still active') : t('runs may overlap')}</span></dd>
            <dt className="label">{t('Late start')}</dt>
            <dd>{op.startingDeadlineSeconds !== undefined ? <><span className="mono">{t('{n} s', { n: op.startingDeadlineSeconds })}</span> <span className="muted">startingDeadlineSeconds</span></> : <span className="muted">{t('no deadline; OpsTide allows 2 min')}</span>}</dd>
            <dt className="label">{t('History kept')}</dt>
            <dd>{rich('{succeeded} succeeded, {failed} failed Job in the cluster', { succeeded: <span className="mono">{op.successfulJobsHistoryLimit}</span>, failed: <span className="mono">{op.failedJobsHistoryLimit}</span> })}</dd>
          </dl>
        </section>
      </div>

      <section aria-label={t('Last 7 days')} style={{ display: 'flex', flexDirection: 'column' }}>
        <Timeline label={t('Last 7 days')} compact labelWidth={120} nowPerLane from={data.days[0].from} to={data.days[0].to} tz={tz} now={data.now} gaps={data.gaps}
          lanes={data.days.map((d) => {
            const ok = d.marks.filter((m) => m.state === 'succeeded').length
            const failed = d.marks.filter((m) => m.state === 'failed').length
            const slow = d.marks.filter((m) => m.slow && m.state !== 'failed').length
            const sum = [ok && t('{n} ok', { n: ok }), failed && t('{n} failed', { n: failed }), slow && t('{n} slow', { n: slow })].filter(Boolean).join(' · ') || '—'
            return { key: d.date, name: shortDay(d.from, tz), meta: sum, from: d.from, to: d.to, marks: d.marks, emphasis: d.date === today }
          })}
          markHref={(m) => (m.runId ? withParams(params, { run: m.runId }) : undefined)} />
        <Legend items={['succeeded', 'failed', 'running', 'expected', 'missed', 'gap', 'slow']} note={t("Bar width is the run's duration.")} />
      </section>

      <div className="split">
        <section aria-label={t('Recent runs')} className="main">
          <h2 className="label section-title">{t('Recent runs')} <span className="note">· {t('Jobs linked by UID, times {tz}', { tz })}</span></h2>
          {perDay.length === 0 ? <div className="empty">{t('No runs seen yet.')}</div> : (
            <div className="table-wrap">
              <table className="data" style={{ minWidth: 720, fontSize: 13 }}>
                <thead>
                  <tr>{['Expected', 'Created', 'Started', 'Finished', 'Result', 'Took', 'Basis'].map((h) => <th key={h} scope="col" className="label" style={{ paddingTop: 10 }}>{t(h)}</th>)}</tr>
                </thead>
                <tbody>
                  {perDay.map((r) => (
                    <tr key={r.id} className={`clickable${r.id === runId ? ' sel' : ''}`} onClick={() => navigate(withParams(params, { run: r.id }), true)}>
                      <td className="mono">{r.scheduledAt ? hhmm(r.scheduledAt, tz) : <span className="muted">{r.basis === 'manual' ? t('by hand') : '—'}</span>}</td>
                      <td className="mono muted">{hhmmss(r.createdAt, tz)}</td>
                      <td className="mono muted">{r.startedAt ? hhmmss(r.startedAt, tz) : '—'}</td>
                      <td className="mono muted">{r.finishedAt ? hhmmss(r.finishedAt, tz) : '—'}</td>
                      <td className={r.status === 'failed' ? 'fail' : undefined} style={{ fontWeight: r.status === 'failed' ? 600 : 400, color: r.status === 'running' ? 'var(--accent)' : undefined }}>{statusWord(r.status)}</td>
                      <td className="mono" style={{ fontWeight: r.slow ? 600 : 400 }}>{r.seconds ? (r.status === 'running' ? t('{d} so far', { d: duration(r.seconds) }) : duration(r.seconds)) : '—'}</td>
                      <td className="muted" style={{ fontSize: 12 }}>{basis(r.status)}{r.slow && expected ? ` · ${t('over {d}', { d: expected })}` : ''}{r.attempts > 1 ? ` · ${t('{n} attempts', { n: r.attempts })}` : ''}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {data.moreRuns && (
            <Link className="button" style={{ alignSelf: 'flex-start', marginTop: 14, display: 'inline-flex', alignItems: 'center', textDecoration: 'none' }}
              href={withParams(params, { before: data.runs[data.runs.length - 1].createdAt, run: null })}>{t('Older runs')}</Link>
          )}
          {before && <Link href={withParams(params, { before: null, run: null })} style={{ marginTop: 10, fontSize: 13 }}>{t('Newest runs')}</Link>}
        </section>
        <aside aria-label={t('Selected run')}>{runId ? <RunPanel id={runId} tz={tz} expected={op.expectedSeconds} /> : <div className="empty">{t('No run selected.')}</div>}</aside>
      </div>
    </div>
  )
}

function basis(status: string): string {
  // The two conditions are Kubernetes' own words; the others are drawn in the interface language.
  if (status === 'succeeded') return 'Complete=True'
  if (status === 'failed') return 'Failed=True'
  const w = ({ running: 'Job active', pending: 'Job created', unknown: 'Job removed unseen' } as Record<string, string>)[status]
  return w ? t(w) : ''
}

function RunPanel({ id, tz, expected }: { id: string; tz: string; expected?: number }) {
  const { data, error } = useApi<RunDetail>(`/api/v1/runs/${encodeURIComponent(id)}`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  const r = data.run
  const when = r.scheduledAt ? t('The {time} run', { time: hhmm(r.scheduledAt, tz) }) : t('This run')
  // The record that says this run failed, where "What changed before this" opens in the Explorer.
  const failedEntry = r.status === 'failed' ? data.entries.find((e) => e.type === 'job.failed') : undefined
  const head = r.status === 'failed' ? t('Run failed') : r.status === 'running' ? t('Running') : r.status === 'unknown' ? t('Outcome unknown') : statusWord(r.status)
  return (
    <>
      <div className="panel-head">
        <span className="label" style={{ color: r.status === 'failed' ? 'var(--fail)' : undefined }}>{head}</span>
        <span className="name">{when}</span>
        <span className="muted" style={{ fontSize: 13 }}>
          {r.status === 'failed' && <>
            {r.attempts > 1 ? `${t('{n} attempts.', { n: r.attempts })} ` : ''}
            {rich(
              r.reason ? (r.seconds ? 'The Job ended with {reason} at {time}, after {took}' : 'The Job ended with {reason} at {time}') : r.seconds ? 'The Job ended at {time}, after {took}' : 'The Job ended at {time}',
              {
                reason: <span className="mono">{r.reason}</span>,
                time: <span className="mono">{hhmmss(r.finishedAt!, tz)}</span>,
                took: <b style={{ color: 'var(--ink)', fontWeight: 600 }}>{duration(r.seconds ?? 0)}</b>,
              },
            )}
            {expected && r.seconds && r.slow ? t(', against an expected {d}', { d: duration(expected) }) : ''}.
          </>}
          {r.status === 'succeeded' && (r.slow && expected
            ? t('Done in {took}, longer than the expected {expected}.', { took: duration(r.seconds ?? 0), expected: duration(expected) })
            : t('Done in {took}.', { took: duration(r.seconds ?? 0) }))}
          {r.status === 'running' && (r.slow && expected
            ? t('Running for {took}, already over the expected {expected}.', { took: duration(r.seconds ?? 0), expected: duration(expected) })
            : t('Running for {took}.', { took: duration(r.seconds ?? 0) }))}
          {r.status === 'unknown' && t('The Job was removed before OpsTide saw it finish.')}
        </span>
        {failedEntry && (
          <Link className="button" style={{ alignSelf: 'flex-start', marginTop: 4 }} href={explorerLink({ rec: recordIdOf(failedEntry), ...rangeAround(failedEntry.at) })}
            title={t('Changes OpsTide recorded before this run failed, on the CronJob and what it is connected to')}>{t('What changed before this')}</Link>
        )}
      </div>
      <div className="tree">
        <span className="label">CronJob → Job → Pods</span>
        {data.operation && <span className="mono">{data.operation.name}</span>}
        <span className="mono" style={{ paddingLeft: 14 }}>└ Job {r.job}</span>
        {(r.pods ?? []).map((p, i, all) => (
          <span key={p.uid} className="mono" style={{ paddingLeft: 28 }}>
            {i === all.length - 1 ? '└' : '├'} Pod …{p.name.slice(-5)}{' '}
            {p.exitCode !== undefined ? <span className={p.exitCode ? 'fail' : 'muted'}>{t('exit {code}', { code: p.exitCode })}</span> : <span className="muted">{p.phase}</span>}
            {p.reason ? <span className="muted"> · {p.reason}</span> : null}
            {p.finishedAt && <span className="muted"> {hhmm(p.finishedAt, tz)}</span>}
          </span>
        ))}
        {!r.pods?.length && <span className="muted" style={{ paddingLeft: 28 }}>{t('no pods seen')}</span>}
      </div>
      <dl className="facts" style={{ gridTemplateColumns: '100px minmax(0, 1fr)' }}>
        <dt className="label">{t('Scheduled')}</dt>
        <dd>{r.scheduledAt ? <><span className="mono">{hhmmss(r.scheduledAt, tz)}</span> <span className="muted">{r.basis === 'annotation' ? t("from the Job's cronjob-scheduled-timestamp annotation") : t("from the Job's name")}</span></> : <span className="muted">{r.basis === 'manual' ? t('started by hand (kubectl create job --from)') : t('unknown')}</span>}</dd>
        <dt className="label">{t('Outcome')}</dt>
        <dd>{r.status === 'succeeded' || r.status === 'failed' ? rich('Derived from {condition}', { condition: <span className="mono">{basis(r.status)}</span> }) : basis(r.status)}</dd>
        <dt className="label">{t('Identity')}</dt>
        <dd className="mono">job {r.id}</dd>
      </dl>
      <RunEvents id={id} tz={tz} />
      {data.entries.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12 }}>
          <span className="label">{t('Log entries')}</span>
          {data.entries.map((e) => (
            <span key={e.id} className="mono muted"><span style={{ color: 'var(--ink)' }}>{hhmmss(e.at, tz)}</span> {e.summary}</span>
          ))}
        </div>
      )}
      <span className="muted" style={{ fontSize: 12 }}>{t('OpsTide does not keep container logs.')} <Link href={explorerLink({ q: r.job, routine: '1', ...rangeAround(r.createdAt) })}>{t('All records for this Job')}</Link></span>
    </>
  )
}

// The Kubernetes Events linked to a run, as Kubernetes reported them. An Event is here because it names the Job by
// UID or names a Pod whose ownerReference is the Job, never because of its name or its time.
function RunEvents({ id, tz }: { id: string; tz: string }) {
  const { data } = useApi<RunEvidence>(`/api/v1/runs/${encodeURIComponent(id)}/evidence`)
  if (!data) return null
  // English keys; drawn through t().
  const words: Record<string, string> = { DIRECT: 'names the Job by UID', OWNERSHIP: 'names a Pod owned by the Job', SAME_RESOURCE_CONTEXT: 'same resource context', UNRESOLVED: 'unresolved' }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, fontSize: 13 }}>
      <span className="label">{t('Kubernetes Events')} <span className="muted">· {t('as reported, linked by UID and ownerReference')}</span> <Link href={explorerLink({ run: id, range: '7d' })}>{t('Records of this run in Explorer')}</Link></span>
      {data.records.length === 0 && <span className="muted">{t('No Kubernetes Event is recorded for this run.')}</span>}
      {data.records.map((e) => {
        const link = e.links.find((l) => l.kind === 'Run')
        const first = e.occurredAt.first, last = e.occurredAt.last
        return (
          <div key={e.id} style={{ display: 'flex', flexDirection: 'column', gap: 2, borderTop: '1px solid var(--rule)', paddingTop: 6 }}>
            <span><WorkspaceToggle id={e.recordId} compact />{' '}<b>{e.reason || t('no reason reported')}</b> <span className="muted">· {e.eventType} · {e.resource.kind} <span className="mono">{e.resource.name}</span></span></span>
            {e.message && <span style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{e.message.length > 240 ? `${e.message.slice(0, 240)}…` : e.message}</span>}
            <span className="muted mono" style={{ fontSize: 12 }}>
              {e.count ? t('count {n}', { n: e.count.value }) : t('count not reported')} · {first ? t('first {time}', { time: hhmmss(first, tz) }) : t('first time not reported')}{last ? ` · ${t('last {time}', { time: hhmmss(last, tz) })}` : ''} · {e.source}{link ? ` · ${words[link.relationship] ? t(words[link.relationship]) : link.relationship}` : ''}
            </span>
          </div>
        )
      })}
      {data.notes.map((n, i) => <span key={i} className="muted" style={{ fontSize: 12 }}>{n}</span>)}
      {data.provenance.notes.map((n, i) => <span key={i} className="fail" style={{ fontSize: 12 }}>{n}</span>)}
    </div>
  )
}
