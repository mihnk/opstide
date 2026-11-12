import { query, type OperationRow, type Operations } from '../api'
import { t, tn, ts } from '../i18n'
import { DurationCell, LongerMark } from '../components/Duration'
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

// English keys; drawn through t().
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
  // Under "Needs a look", what needs one only for what happened during a declared maintenance window comes after the
  // rest. It is still listed.
  if (tab === 'attention') rows.sort((a, b) => Number(!!a.maintenance) - Number(!!b.maintenance))

  return (
    <div className="page" style={{ gap: 20 }}>
      <div className="head-row" style={{ gap: '12px 28px' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">{t('CronJobs in this cluster')}</span>
          <h1 className="title">{tn(data.operations.length, '{n} schedule', '{n} schedules')}</h1>
        </div>
        <nav aria-label={t('Filter schedules')} className="tabs">
          {tabs.map(([k, label, f]) => {
            const n = data.operations.filter(f).length
            return (
              <Link key={k} href={withParams(params, { tab: k === 'all' ? null : k })} replace aria-current={tab === k ? 'true' : undefined}
                className={k === 'attention' && n > 0 ? 'alert' : undefined}>
                {t(label)} {n}
              </Link>
            )
          })}
        </nav>
        <span className="grow" />
        <ZoneSelect tz={tz} setTz={setTz} />
      </div>

      {rows.length === 0 ? (
        <div className="empty">{data.operations.length === 0 ? t('OpsTide has not seen a CronJob yet. If it only watches some namespaces, System lists them; a watch that lacks permission is shown there too.') : t('None in this group.')}</div>
      ) : (
        <div className="table-wrap">
          <table className="data compact" style={{ minWidth: 1080 }}>
            <thead>
              <tr>
                {['Operation', 'Schedule', 'Last 7 days', 'Last run', 'Run time', 'Next, expected', 'Owner', 'Criticality'].map((h) => (
                  <th key={h} scope="col" className="label">{t(h)}</th>
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
                        <span className="mono muted fine">{o.namespace}/{o.name}</span>
                        {!!o.attention?.length && <span className="fail fine">{o.attention.map((w) => t(w)).join(' · ')}</span>}
                        {o.maintenance && <span><span className="chip maint" style={{ marginLeft: 0 }} title={ts(o.maintenance.statement)}>{t('during maintenance {name}', { name: o.maintenance.name })}</span></span>}
                      </Link>
                    </td>
                    <td>
                      <span className="mono fine">{o.schedule}</span>
                      <span className="muted sub">
                        {o.scheduleError ? t('cannot be read') : o.timeZone || t('{zone} · no timeZone set', { zone: o.zoneAssumed ?? '' })}{o.suspended ? ` · ${t('suspended')}` : ''}
                      </span>
                    </td>
                    <td>
                      <span className="week" role="img" aria-label={o.week.map((c) => `${c.date}: ${t(cellWords[c.state])}`).join('; ')}>
                        {o.week.map((c) => (
                          <i key={c.date} className={c.state} title={`${shortDay(`${c.date}T12:00:00Z`, 'UTC')}: ${t(cellWords[c.state])}${c.runs ? `, ${t('{n} runs', { n: c.runs })}` : ''}`} />
                        ))}
                      </span>
                    </td>
                    <td>
                      {lr ? (
                        <>
                          <span className={lastState} style={{ fontWeight: lr.status === 'failed' ? 500 : 400 }}>{statusWord(lr.status)}</span>
                          <span className="mono muted sub">
                            {relativeTime(lr.scheduledAt ?? lr.createdAt, data.now, tz)}{lr.seconds ? ` · ${duration(lr.seconds)}` : ''}
                          </span>
                        </>
                      ) : (
                        <span className="muted">{t('none seen')}</span>
                      )}
                    </td>
                    <td><DurationCell d={o.duration} /></td>
                    <td className="mono fine" style={{ color: o.next ? undefined : 'var(--muted)' }}>
                      {o.next ? relativeTime(o.next, data.now, tz) : o.suspended ? `— ${t('suspended')}` : '—'}
                    </td>
                    <td style={{ color: o.owner ? undefined : 'var(--muted)' }}>{o.owner || '—'}</td>
                    <td style={{ textTransform: 'capitalize', color: o.criticality ? undefined : 'var(--muted)', fontWeight: o.criticality === 'critical' || o.criticality === 'high' ? 500 : 400 }}>{o.criticality || '—'}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      <div className="legend" style={{ paddingTop: 0 }}>
        <span><span className="week"><i className="ok" /></span>{t('every observed run succeeded')}</span>
        <span><span className="week"><i className="failed" /></span>{t('a run failed')}</span>
        <span><span className="week"><i className="missed" /></span>{t('did not start')}</span>
        <span><span className="week"><i className="unconfirmed" /></span>{t('not confirmed')}</span>
        <span><span className="week"><i /></span>{t('nothing due, or before OpsTide was watching')}</span>
        <span>{t('Run time is the median (p50) of the succeeded runs of the last 7 days, compared with the 23 days before.')}</span>
        <span><LongerMark />{t('longer than its baseline: a median at least 30% longer, with {n} runs or more in each period', { n: 5 })}</span>
        <span>{t('Next runs are computed from the schedule and its time zone; Kubernetes may start them late or skip them.')}</span>
      </div>
    </div>
  )
}

export function statusWord(s: string): string {
  const w = ({ succeeded: 'Succeeded', failed: 'Failed', running: 'Running', pending: 'Created, not started', unknown: 'Outcome unknown' } as Record<string, string>)[s]
  return w ? t(w) : s
}
