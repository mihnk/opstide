import { query, type OperationRow, type Operations } from '../api'
import { Failed, Loading } from '../components/Shell'
import { useApi } from '../live'
import { Link, navigate, withParams } from '../router'
import { duration, relativeTime, shortDay } from '../time'
import { ZoneSelect } from './common'

const tabs = [
  ['all', 'All', () => true],
  ['attention', 'Needs a look', (o: OperationRow) => !!o.attention?.length],
  ['suspended', 'Suspended', (o: OperationRow) => !!o.suspended],
  ['undescribed', 'Not described', (o: OperationRow) => !o.description && !o.displayName],
] as const

const cellWords: Record<string, string> = {
  ok: 'every observed run succeeded', failed: 'a run failed', missed: 'a run did not start',
  unconfirmed: 'not confirmed', suspended: 'suspended', none: 'nothing due',
}

export function Schedules({ params, tz, setTz }: { params: URLSearchParams; tz: string; setTz: (z: string) => void }) {
  const { data, error } = useApi<Operations>(`/api/v1/operations${query({ tz })}`)
  const tab = params.get('tab') ?? 'all'
  if (error && !data) return <div className="page"><Failed error={error} /></div>
  if (!data) return <div className="page"><Loading /></div>
  const filter = tabs.find(([k]) => k === tab)?.[2] ?? (() => true)
  const rows = data.operations.filter(filter)

  return (
    <div className="page" style={{ gap: 20 }}>
      <div className="head-row" style={{ gap: '12px 28px' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">CronJobs in this cluster</span>
          <h1 className="title">{data.operations.length} schedule{data.operations.length === 1 ? '' : 's'}</h1>
        </div>
        <nav aria-label="Filter schedules" className="tabs">
          {tabs.map(([k, label, f]) => {
            const n = data.operations.filter(f).length
            return (
              <Link key={k} href={withParams(params, { tab: k === 'all' ? null : k })} replace aria-current={tab === k ? 'true' : undefined}
                className={k === 'attention' && n > 0 ? 'alert' : undefined}>
                {label} {n}
              </Link>
            )
          })}
        </nav>
        <span className="grow" />
        <ZoneSelect tz={tz} setTz={setTz} />
      </div>

      {rows.length === 0 ? (
        <div className="empty">{data.operations.length === 0 ? 'No CronJobs seen yet.' : 'None in this group.'}</div>
      ) : (
        <div className="table-wrap">
          <table className="data" style={{ minWidth: 1060 }}>
            <thead>
              <tr>
                {['Operation', 'Schedule', 'Last 7 days', 'Last run', 'Next, expected', 'Owner'].map((h) => (
                  <th key={h} scope="col" className="label">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((o) => {
                const href = `/operations/${encodeURIComponent(o.id)}`
                const lr = o.lastRun
                const lastState = lr?.status === 'failed' ? 'fail' : lr?.status === 'unknown' ? 'muted' : ''
                return (
                  <tr key={o.id} className="clickable" onClick={(e) => { if (!(e.target as HTMLElement).closest('a')) navigate(href) }}>
                    <td>
                      <Link href={href} style={{ display: 'flex', flexDirection: 'column', color: 'var(--ink)', textDecoration: 'none' }}>
                        <span style={{ fontWeight: 500, color: o.displayName ? undefined : 'var(--muted)' }}>{o.displayName || o.name}</span>
                        <span className="mono muted" style={{ fontSize: 12 }}>{o.namespace}/{o.name}</span>
                        {!!o.attention?.length && <span className="fail" style={{ fontSize: 12 }}>{o.attention.join(' · ')}</span>}
                      </Link>
                    </td>
                    <td>
                      <span className="mono" style={{ fontSize: 13 }}>{o.schedule}</span>
                      <span className="muted" style={{ display: 'block', fontSize: 12 }}>
                        {o.scheduleError ? 'cannot be read' : o.timeZone || `${o.zoneAssumed} · no timeZone set`}{o.suspended ? ' · suspended' : ''}
                      </span>
                    </td>
                    <td>
                      <span className="week" role="img" aria-label={o.week.map((c) => `${c.date}: ${cellWords[c.state]}`).join('; ')}>
                        {o.week.map((c) => (
                          <i key={c.date} className={c.state} title={`${shortDay(`${c.date}T12:00:00Z`, 'UTC')}: ${cellWords[c.state]}${c.runs ? `, ${c.runs} runs` : ''}`} />
                        ))}
                      </span>
                    </td>
                    <td>
                      {lr ? (
                        <>
                          <span className={lastState} style={{ fontWeight: lr.status === 'failed' ? 500 : 400 }}>{statusWord(lr.status)}</span>
                          <span className="mono muted" style={{ display: 'block', fontSize: 12 }}>
                            {relativeTime(lr.scheduledAt ?? lr.createdAt, data.now, tz)}{lr.seconds ? ` · ${duration(lr.seconds)}` : ''}
                          </span>
                        </>
                      ) : (
                        <span className="muted">none seen</span>
                      )}
                    </td>
                    <td className="mono" style={{ fontSize: 13, color: o.next ? undefined : 'var(--muted)' }}>
                      {o.next ? relativeTime(o.next, data.now, tz) : o.suspended ? '— suspended' : '—'}
                    </td>
                    <td style={{ color: o.owner ? undefined : 'var(--muted)' }}>{o.owner || '—'}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      <div className="legend" style={{ paddingTop: 0 }}>
        <span><span className="week"><i className="ok" /></span>every observed run succeeded</span>
        <span><span className="week"><i className="failed" /></span>a run failed</span>
        <span><span className="week"><i className="missed" /></span>did not start</span>
        <span><span className="week"><i className="unconfirmed" /></span>not confirmed</span>
        <span><span className="week"><i /></span>nothing due, or before OpsTide was watching</span>
        <span>Next runs are computed from the schedule and its time zone; Kubernetes may start them late or skip them.</span>
      </div>
    </div>
  )
}

export function statusWord(s: string): string {
  return ({ succeeded: 'Succeeded', failed: 'Failed', running: 'Running', pending: 'Created, not started', unknown: 'Outcome unknown' } as Record<string, string>)[s] ?? s
}
