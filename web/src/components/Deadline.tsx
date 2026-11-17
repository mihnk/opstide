import type { Deadline } from '../api'
import { t } from '../i18n'
import { duration, relativeTime } from '../time'

/** A must-succeed-within limit as people say it: "26 h", "8 d", "90 min", never broken between number and unit. */
export function limitWords(seconds: number): string {
  return limitText(seconds).replace(/ /g, '\u00a0')
}

function limitText(seconds: number): string {
  if (seconds >= 2 * 86400 && seconds % 86400 === 0) return t('{n} d', { n: seconds / 86400 })
  if (seconds >= 3600 && seconds % 3600 === 0) return t('{n} h', { n: seconds / 3600 })
  if (seconds % 60 === 0 && seconds < 3600) return t('{n} min', { n: seconds / 60 })
  return duration(seconds)
}

const unknownWords: Record<string, string> = {
  before_first_observation: 'not known yet: OpsTide has watched for less than that',
  outside_retention: 'not known: longer than the history OpsTide keeps',
  not_watched: 'not known: OpsTide was not watching for part of it, and a succeeded Job could have been deleted before it looked again',
}

/** One line for the Schedules table, under the last run. Nothing for a suspended CronJob: it is not judged. */
export function DeadlineShort({ d, now, tz }: { d: Deadline; now: string; tz: string }) {
  const limit = limitWords(d.withinSeconds)
  switch (d.state) {
    case 'overdue':
      return <span className="fail sub" style={{ fontWeight: 600 }}>{t('overdue · limit {d}', { d: limit })}</span>
    case 'met':
      return <span className="muted sub">{t('on time · limit {d}', { d: limit })}</span>
    case 'waiting':
      return <span className="muted sub">{t('first success due {when}', { when: d.dueBy ? relativeTime(d.dueBy, now, tz) : '' })}</span>
    case 'unknown':
      return <span className="muted sub" title={t(unknownWords[d.reason ?? ''] ?? 'not known')}>{t('limit {d} · not known', { d: limit })}</span>
    default:
      return null
  }
}

/** The CronJob page's account of its limit: where it stands, then the times it is counted from. */
export function DeadlineFull({ d, now, tz }: { d: Deadline; now: string; tz: string }) {
  const limit = limitWords(d.withinSeconds)
  const at = (x?: string) => (x ? relativeTime(x, now, tz) : '')
  const last = d.lastSuccess ? t('last success {when}', { when: at(d.lastSuccess) }) : t('no success recorded')
  switch (d.state) {
    case 'overdue':
      return (
        <>
          <b className="fail">{d.dueBy ? t('Overdue since {when}, limit {d}', { when: at(d.dueBy), d: limit }) : t('Overdue, limit {d}', { d: limit })}</b>
          <span className="muted"> · {last}</span>
        </>
      )
    case 'met':
      return <>{t('On time, limit {d}', { d: limit })} <span className="muted">· {last} · {t('due by {when}', { when: at(d.dueBy) })}</span></>
    case 'waiting':
      return <>{t('No success yet, limit {d}', { d: limit })} <span className="muted">· {t('first success due {when}', { when: at(d.dueBy) })}</span></>
    case 'suspended':
      return <>{t('Limit {d}', { d: limit })} <span className="muted">· {t('not judged while suspended')}</span></>
    default:
      return (
        <>
          {t('Limit {d}', { d: limit })}{' '}
          <span className="muted">· {t(unknownWords[d.reason ?? ''] ?? 'not known')}{d.knownFrom ? ` · ${t('known from {when}', { when: at(d.knownFrom) })}` : ''}</span>
        </>
      )
  }
}
