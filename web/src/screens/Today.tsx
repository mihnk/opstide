import { query, type Day } from '../api'
import { Failed, Loading } from '../components/Shell'
import { Legend, Timeline, type TimelineLane } from '../components/Timeline'
import { useApi } from '../live'
import { daySentence, exceptionText } from '../narrative'
import { Link, navigate, withParams } from '../router'
import { hhmm, isoDate, longDay, offset, shiftDate } from '../time'
import { ZoneSelect } from './common'

export function Today({ params, tz, setTz }: { params: URLSearchParams; tz: string; setTz: (z: string) => void }) {
  const date = params.get('date') ?? ''
  const { data, error } = useApi<Day>(`/api/v1/day${query({ date, tz })}`)
  if (error && !data) return <div className="page"><Failed error={error} /></div>
  if (!data) return <div className="page"><Loading /></div>

  const today = data.date === isoDate(data.now, tz)
  const lanes: TimelineLane[] = data.lanes.map((l) => ({
    key: l.operationId,
    name: l.name,
    meta: `${l.namespace} · ${l.schedule}${l.suspended ? ' · suspended' : ''}`,
    href: `/operations/${encodeURIComponent(l.operationId)}`,
    marks: l.marks,
  }))
  if (data.rollouts.length) {
    lanes.push({
      key: 'rollouts', name: 'Rollouts', meta: 'Deployments, observed', href: '/log?show=rollouts',
      marks: data.rollouts.map((r) => ({ start: r.at, state: 'running' as const, why: `${hhmm(r.at, tz)} ${r.summary}` })),
      markTitle: (m) => m.why ?? '',
    })
  }
  const day = (d: string) => withParams(params, { date: d === isoDate(data.now, tz) ? null : d })

  return (
    <div className="page">
      <div className="head-row">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">Day</span>
          <span className="daynav">
            <Link href={day(shiftDate(data.date, -1))} aria-label="Previous day">‹</Link>
            <h1 className="title">{longDay(data.from, tz)}</h1>
            {today ? <span className="off" aria-hidden="true">›</span> : <Link href={day(shiftDate(data.date, 1))} aria-label="Next day">›</Link>}
          </span>
        </div>
        <span className="grow" />
        {!today && <Link href={day(isoDate(data.now, tz))} style={{ paddingBottom: 8 }}>Back to today</Link>}
        <ZoneSelect tz={tz} setTz={setTz} />
      </div>

      <p className="sentence">{daySentence(data, today)}</p>

      <section aria-label="Day chart" style={{ display: 'flex', flexDirection: 'column' }}>
        {lanes.length === 0 ? (
          <div className="empty">
            {data.idle > 0 ? `Nothing was due on this day. ${data.idle} CronJob${data.idle === 1 ? ' runs' : 's run'} on other days or ${data.idle === 1 ? 'is' : 'are'} suspended.` : 'No CronJobs seen yet. Once the collector sees CronJobs, they appear here.'}
          </div>
        ) : (
          <>
            <Timeline label="Operation" from={data.from} to={data.to} tz={tz} now={today ? data.now : undefined} gaps={data.gaps} lanes={lanes}
              markHref={(m, lane) => (lane.key === 'rollouts' ? undefined : `/operations/${encodeURIComponent(lane.key)}${m.runId ? `?run=${encodeURIComponent(m.runId)}` : ''}`)} />
            <Legend note={`Bar width is the run's duration.${data.idle ? ` ${data.idle} CronJob${data.idle === 1 ? '' : 's'} had nothing due.` : ''} Times ${offset(tz, new Date(data.from))}.`} />
          </>
        )}
      </section>

      <div className="two">
        <section aria-label="Exceptions" className="rows">
          <h2 className="label section-title">Exceptions</h2>
          {data.exceptions.length === 0 && <div className="empty">Nothing needs a look{data.gaps.length ? ', as far as OpsTide could see' : ''}.</div>}
          {data.exceptions.map((e, i) => {
            const t = exceptionText(e, tz)
            const red = e.kind === 'failed' || e.kind === 'missed'
            return (
              <Link key={i} className="row" style={{ gridTemplateColumns: '56px minmax(0, 1fr)' }}
                href={`/operations/${encodeURIComponent(e.operationId)}${e.runId ? `?run=${encodeURIComponent(e.runId)}` : ''}`}>
                <span className={`mono${red ? ' fail' : ' muted'}`} style={{ fontWeight: 500 }}>{hhmm(e.at, tz)}</span>
                <span style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                  <span style={{ fontWeight: 500 }}>{t.title}</span>
                  <span className="muted">{t.detail}</span>
                </span>
              </Link>
            )
          })}
        </section>

        <section aria-label="Coming up" className="rows">
          <h2 className="label section-title">Coming up <span className="note">· from the schedules</span></h2>
          {!today && <div className="empty">Shown for today only.</div>}
          {today && data.upcoming.length === 0 && <div className="empty">Nothing more is expected today.</div>}
          {today &&
            data.upcoming.map((u) => (
              <Link key={u.operationId} className="row" href={`/operations/${encodeURIComponent(u.operationId)}`}>
                <span className="mono">{hhmm(u.at, tz)}</span>
                <span>{u.name}</span>
                <span className="mono muted" style={{ fontSize: 12 }}>{u.namespace}</span>
              </Link>
            ))}
          <a href="/schedules" onClick={(e) => { e.preventDefault(); navigate('/schedules') }} style={{ paddingTop: 12, fontSize: 13 }}>All schedules</a>
        </section>
      </div>
    </div>
  )
}
