import { query, type OperationDetail, type RunDetail } from '../api'
import { Failed, Loading } from '../components/Shell'
import { Legend, Timeline } from '../components/Timeline'
import { useApi } from '../live'
import { Link, navigate, withParams } from '../router'
import { duration, hhmm, hhmmss, isoDate, relativeTime, shortDay } from '../time'
import { statusWord } from './Schedules'

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
          <Link href="/schedules" style={{ fontSize: 13, textDecoration: 'none' }}>‹ Schedules</Link>
          <h1 className="title">{op.displayName || op.name}</h1>
          <span className="mono muted" style={{ fontSize: 13 }}>{op.namespace}/{op.name} · CronJob · uid {op.id.slice(0, 4)}…{op.id.slice(-4)}</span>
        </div>
        <span style={{ display: 'flex', flexDirection: 'column', gap: 2, paddingBottom: 4 }}>
          <span className="label">Last run</span>
          <span>
            {last ? (
              <>
                <b className={last.status === 'failed' ? 'fail' : undefined} style={{ fontWeight: 600 }}>{statusWord(last.status)}</b>{' '}
                <span className="mono">{hhmm(last.finishedAt ?? last.createdAt, tz)}</span>
              </>
            ) : (
              <span className="muted">none seen</span>
            )}
            <span className="muted"> · {op.deletedAt ? 'CronJob deleted' : data.next ? <>next expected <span className="mono">{relativeTime(data.next, data.now, tz)}</span></> : op.suspended ? 'suspended' : 'no next run'}</span>
          </span>
        </span>
        <span className="grow" />
        <button type="button" className="button" disabled title="No investigator is configured. See System.">Investigate · not configured</button>
      </div>

      <div className="two">
        <section aria-label="What it does" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <h2 className="label section-title">What it does</h2>
          {op.description ? (
            <p style={{ margin: 0, fontSize: 16, lineHeight: 1.5, whiteSpace: 'pre-line' }}>{op.description}</p>
          ) : (
            <p className="muted" style={{ margin: 0 }}>Not described. Add <span className="mono">opstide.mihnk.org/description</span> to the CronJob in its chart.</p>
          )}
          <dl className="facts">
            <dt className="label">Owner</dt><dd>{op.owner || <span className="muted">not set</span>}</dd>
            <dt className="label">Criticality</dt><dd style={{ textTransform: 'capitalize' }}>{op.criticality || <span className="muted" style={{ textTransform: 'none' }}>not set</span>}</dd>
            <dt className="label">Runbook</dt>
            <dd>{op.runbookUrl ? <a href={op.runbookUrl} target="_blank" rel="noopener noreferrer">{op.runbookUrl.replace(/^https:\/\//, '')}</a> : <span className="muted">not set</span>}</dd>
          </dl>
          {!!op.metadataProblems?.length && <span className="fail" style={{ fontSize: 12 }}>{op.metadataProblems.join('. ')}.</span>}
          <span className="muted" style={{ fontSize: 12 }}>From the CronJob's <span className="mono">opstide.mihnk.org/*</span> annotations. Edit them in the chart; OpsTide only reads them.</span>
        </section>

        <section aria-label="What is expected" style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <h2 className="label section-title">What is expected</h2>
          <p style={{ margin: 0, fontSize: 16, lineHeight: 1.5 }}>
            {op.scheduleError ? 'The schedule cannot be read: ' + op.scheduleError : (
              <>{runsPerDay ? `${runsPerDay} expected time${runsPerDay === 1 ? '' : 's'} today` : 'Nothing expected today'}{expected ? <>, each done in <b>up to {expected}</b></> : ''}.</>
            )}
          </p>
          <dl className="facts">
            <dt className="label">Schedule</dt>
            <dd className="mono">{op.schedule} <span className="muted">{op.timeZone || `${op.zoneAssumed} (no timeZone set; the controller's zone)`}</span></dd>
            <dt className="label">Overlap</dt>
            <dd><span className="mono">{op.concurrency}</span> <span className="muted">· {op.concurrency === 'Forbid' ? 'a run is skipped while the previous one is active' : op.concurrency === 'Replace' ? 'a new run replaces one still active' : 'runs may overlap'}</span></dd>
            <dt className="label">Late start</dt>
            <dd>{op.startingDeadlineSeconds !== undefined ? <><span className="mono">{op.startingDeadlineSeconds} s</span> <span className="muted">startingDeadlineSeconds</span></> : <span className="muted">no deadline; OpsTide allows 2 min</span>}</dd>
            <dt className="label">History kept</dt>
            <dd><span className="mono">{op.successfulJobsHistoryLimit}</span> succeeded, <span className="mono">{op.failedJobsHistoryLimit}</span> failed Job in the cluster</dd>
          </dl>
        </section>
      </div>

      <section aria-label="Last 7 days" style={{ display: 'flex', flexDirection: 'column' }}>
        <Timeline label="Last 7 days" compact labelWidth={120} nowPerLane from={data.days[0].from} to={data.days[0].to} tz={tz} now={data.now} gaps={data.gaps}
          lanes={data.days.map((d) => {
            const ok = d.marks.filter((m) => m.state === 'succeeded').length
            const failed = d.marks.filter((m) => m.state === 'failed').length
            const slow = d.marks.filter((m) => m.slow && m.state !== 'failed').length
            const sum = [ok && `${ok} ok`, failed && `${failed} failed`, slow && `${slow} slow`].filter(Boolean).join(' · ') || '—'
            return { key: d.date, name: shortDay(d.from, tz), meta: sum, from: d.from, to: d.to, marks: d.marks, emphasis: d.date === today }
          })}
          markHref={(m) => (m.runId ? withParams(params, { run: m.runId }) : undefined)} />
        <Legend items={['succeeded', 'failed', 'running', 'expected', 'missed', 'unconfirmed', 'gap', 'slow']} note="Bar width is the run's duration." />
      </section>

      <div className="split">
        <section aria-label="Recent runs" className="main">
          <h2 className="label section-title">Recent runs <span className="note">· Jobs linked by UID, times {tz}</span></h2>
          {perDay.length === 0 ? <div className="empty">No runs seen yet.</div> : (
            <div className="table-wrap">
              <table className="data" style={{ minWidth: 720, fontSize: 13 }}>
                <thead>
                  <tr>{['Expected', 'Created', 'Started', 'Finished', 'Result', 'Took', 'Basis'].map((h) => <th key={h} scope="col" className="label" style={{ paddingTop: 10 }}>{h}</th>)}</tr>
                </thead>
                <tbody>
                  {perDay.map((r) => (
                    <tr key={r.id} className={`clickable${r.id === runId ? ' sel' : ''}`} onClick={() => navigate(withParams(params, { run: r.id }), true)}>
                      <td className="mono">{r.scheduledAt ? hhmm(r.scheduledAt, tz) : <span className="muted">{r.basis === 'manual' ? 'by hand' : '—'}</span>}</td>
                      <td className="mono muted">{hhmmss(r.createdAt, tz)}</td>
                      <td className="mono muted">{r.startedAt ? hhmmss(r.startedAt, tz) : '—'}</td>
                      <td className="mono muted">{r.finishedAt ? hhmmss(r.finishedAt, tz) : '—'}</td>
                      <td className={r.status === 'failed' ? 'fail' : undefined} style={{ fontWeight: r.status === 'failed' ? 600 : 400, color: r.status === 'running' ? 'var(--accent)' : undefined }}>{statusWord(r.status)}</td>
                      <td className="mono" style={{ fontWeight: r.slow ? 600 : 400 }}>{r.seconds ? `${duration(r.seconds)}${r.status === 'running' ? ' so far' : ''}` : '—'}</td>
                      <td className="muted" style={{ fontSize: 12 }}>{basis(r.status)}{r.slow && expected ? ` · over ${expected}` : ''}{r.attempts > 1 ? ` · ${r.attempts} attempts` : ''}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {data.moreRuns && (
            <Link className="button" style={{ alignSelf: 'flex-start', marginTop: 14, display: 'inline-flex', alignItems: 'center', textDecoration: 'none' }}
              href={withParams(params, { before: data.runs[data.runs.length - 1].createdAt, run: null })}>Older runs</Link>
          )}
          {before && <Link href={withParams(params, { before: null, run: null })} style={{ marginTop: 10, fontSize: 13 }}>Newest runs</Link>}
        </section>
        <aside aria-label="Selected run">{runId ? <RunPanel id={runId} tz={tz} expected={op.expectedSeconds} /> : <div className="empty">No run selected.</div>}</aside>
      </div>
    </div>
  )
}

function basis(status: string): string {
  return ({ succeeded: 'Complete=True', failed: 'Failed=True', running: 'Job active', pending: 'Job created', unknown: 'Job removed unseen' } as Record<string, string>)[status] ?? ''
}

function RunPanel({ id, tz, expected }: { id: string; tz: string; expected?: number }) {
  const { data, error } = useApi<RunDetail>(`/api/v1/runs/${encodeURIComponent(id)}`)
  if (error && !data) return <Failed error={error} />
  if (!data) return <Loading />
  const r = data.run
  const when = r.scheduledAt ? `The ${hhmm(r.scheduledAt, tz)} run` : 'This run'
  const head = r.status === 'failed' ? 'Run failed' : r.status === 'running' ? 'Running' : r.status === 'unknown' ? 'Outcome unknown' : statusWord(r.status)
  return (
    <>
      <div className="panel-head">
        <span className="label" style={{ color: r.status === 'failed' ? 'var(--fail)' : undefined }}>{head}</span>
        <span className="name">{when}</span>
        <span className="muted" style={{ fontSize: 13 }}>
          {r.status === 'failed' && <>{r.attempts > 1 ? `${r.attempts} attempts. ` : ''}The Job ended {r.reason ? <>with <span className="mono">{r.reason}</span> </> : ''}at <span className="mono">{hhmmss(r.finishedAt!, tz)}</span>{r.seconds ? <>, after <b style={{ color: 'var(--ink)', fontWeight: 600 }}>{duration(r.seconds)}</b></> : ''}{expected && r.seconds && r.slow ? `, against an expected ${duration(expected)}` : ''}.</>}
          {r.status === 'succeeded' && <>Done in {duration(r.seconds ?? 0)}{r.slow && expected ? `, longer than the expected ${duration(expected)}` : ''}.</>}
          {r.status === 'running' && <>Running for {duration(r.seconds ?? 0)}{r.slow && expected ? `, already over the expected ${duration(expected)}` : ''}.</>}
          {r.status === 'unknown' && <>The Job was removed before OpsTide saw it finish.</>}
        </span>
      </div>
      <div className="tree">
        <span className="label">CronJob → Job → Pods</span>
        {data.operation && <span className="mono">{data.operation.name}</span>}
        <span className="mono" style={{ paddingLeft: 14 }}>└ Job {r.job}</span>
        {(r.pods ?? []).map((p, i, all) => (
          <span key={p.uid} className="mono" style={{ paddingLeft: 28 }}>
            {i === all.length - 1 ? '└' : '├'} Pod …{p.name.slice(-5)}{' '}
            {p.exitCode !== undefined ? <span className={p.exitCode ? 'fail' : 'muted'}>exit {p.exitCode}</span> : <span className="muted">{p.phase}</span>}
            {p.finishedAt && <span className="muted"> {hhmm(p.finishedAt, tz)}</span>}
          </span>
        ))}
        {!r.pods?.length && <span className="muted" style={{ paddingLeft: 28 }}>no pods seen</span>}
      </div>
      <dl className="facts" style={{ gridTemplateColumns: '100px minmax(0, 1fr)' }}>
        <dt className="label">Scheduled</dt>
        <dd>{r.scheduledAt ? <><span className="mono">{hhmmss(r.scheduledAt, tz)}</span> <span className="muted">from the Job's {r.basis === 'annotation' ? 'cronjob-scheduled-timestamp annotation' : 'name'}</span></> : <span className="muted">{r.basis === 'manual' ? 'started by hand (kubectl create job --from)' : 'unknown'}</span>}</dd>
        <dt className="label">Outcome</dt>
        <dd>{r.status === 'succeeded' || r.status === 'failed' ? <>Derived from <span className="mono">{basis(r.status)}</span></> : basis(r.status)}</dd>
        <dt className="label">Identity</dt>
        <dd className="mono">job {r.id}</dd>
      </dl>
      {data.entries.length > 0 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4, fontSize: 12 }}>
          <span className="label">Log entries</span>
          {data.entries.map((e) => (
            <span key={e.id} className="mono muted"><span style={{ color: 'var(--ink)' }}>{hhmmss(e.at, tz)}</span> {e.summary}</span>
          ))}
        </div>
      )}
      <span className="muted" style={{ fontSize: 12 }}>OpsTide does not keep container logs. <Link href={`/log?q=${encodeURIComponent(r.job)}&range=30d`}>All entries for this Job</Link></span>
    </>
  )
}
