import type { DurationPeriod, DurationTrend } from '../api'
import { lang, locale, t, tn } from '../i18n'
import { duration, hhmm, shortDay } from '../time'

// A CronJob's run durations, recent (last 7 days) against its baseline (the 23 days before). Numbers and a neutral
// marker only: OpsTide never says a job got worse.

/** "+34%": how much longer (or shorter) the recent median is than the baseline's. */
export function changeText(ratio: number): string {
  const pct = Math.round((ratio - 1) * 100)
  const sign = pct > 0 ? '+' : pct < 0 ? '−' : '±'
  const n = Math.abs(pct).toLocaleString(locale())
  return lang() === 'tr' ? `${sign}%${n}` : `${sign}${n}%`
}

/** The marker drawn beside a duration that is longer than its baseline, in the colour of "longer than expected". */
export function LongerMark() {
  return <i className="longer-mark" aria-hidden="true" />
}

/** The Schedules table's compact cell: "p50 3m 10s · 7d", then the comparison or why there is none. */
export function DurationCell({ d }: { d: DurationTrend }) {
  const r = d.recent
  const b = d.baseline
  const title = [periodTitle(t('Last 7 days'), r), periodTitle(t('Baseline'), b)].join('\n')
  if (r.p50Seconds === null) {
    return (
      <span className="muted" style={{ fontSize: 12 }} title={title}>{t('no succeeded run in 7 days')}</span>
    )
  }
  return (
    <span title={title}>
      <span className="mono" style={{ fontSize: 13 }}>{t('p50 {d} · 7d', { d: duration(r.p50Seconds) })}</span>
      <span className="muted" style={{ display: 'block', fontSize: 12 }}>
        {!d.enough ? t('too few runs to compare') : d.longer && d.ratio !== null ? (
          <span style={{ color: 'var(--ink)' }}><LongerMark />{t('longer than its baseline ({change})', { change: changeText(d.ratio) })}</span>
        ) : b.p50Seconds !== null ? t('baseline p50 {d}', { d: duration(b.p50Seconds) }) : ''}
        {!r.whole ? ` · ${t('not whole')}` : ''}
      </span>
    </span>
  )
}

function periodTitle(name: string, p: DurationPeriod): string {
  if (p.p50Seconds === null || p.p95Seconds === null) return `${name}: ${t('no succeeded run')}`
  return `${name}: ${tn(p.runs, '{n} run', '{n} runs')}, p50 ${duration(p.p50Seconds)}, p95 ${duration(p.p95Seconds)}${p.whole ? '' : ` (${t('not whole')})`}`
}

function coverageText(p: DurationPeriod): string {
  if (p.whole) return t('watched throughout')
  const parts: string[] = []
  if (p.shortenedByRetention) parts.push(t('shortened to the history kept'))
  if (p.notWatchedSeconds > 0) parts.push(t('{d} not watched', { d: duration(p.notWatchedSeconds) }))
  return `${t('not whole')}${parts.length ? `: ${parts.join(', ')}` : ''}`
}

/** The Operation page's section: both periods side by side, the comparison, and the latest runs as small bars. */
export function DurationSection({ d, tz }: { d: DurationTrend; tz: string }) {
  const rows: [string, DurationPeriod][] = [[t('Last 7 days'), d.recent], [t('The 23 days before'), d.baseline]]
  const points = d.points ?? []
  const max = Math.max(1, ...points.map((p) => p.seconds))
  return (
    <section aria-label={t('Run time')} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <h2 className="label section-title">{t('Run time')} <span className="note">· {t('succeeded runs, by when they started')}</span></h2>
      <p style={{ margin: 0, fontSize: 15, lineHeight: 1.5 }}>
        {!d.enough ? (
          t('Too few runs to compare: each period needs at least {n} succeeded runs with a known start and end.', { n: d.minRuns })
        ) : d.ratio !== null && d.longer ? (
          <><LongerMark />{t('Recent p50 is {change} against the baseline p50: longer than its baseline.', { change: changeText(d.ratio) })}</>
        ) : d.ratio !== null ? (
          t('Recent p50 is {change} against the baseline p50.', { change: changeText(d.ratio) })
        ) : null}
      </p>
      <div className="table-wrap">
        <table className="data compact" style={{ fontSize: 13, minWidth: 560 }}>
          <thead>
            <tr>{['Period', 'Runs', 'p50', 'p95', 'Coverage'].map((h) => <th key={h} scope="col" className="label">{t(h)}</th>)}</tr>
          </thead>
          <tbody>
            {rows.map(([name, p]) => (
              <tr key={name}>
                <td>{name} <span className="muted mono" style={{ display: 'block', fontSize: 12 }}>{shortDay(p.from, tz)} – {shortDay(p.to, tz)}</span></td>
                <td className="mono">{p.runs}</td>
                <td className="mono">{p.p50Seconds !== null ? duration(p.p50Seconds) : '—'}</td>
                <td className="mono">{p.p95Seconds !== null ? duration(p.p95Seconds) : '—'}</td>
                <td className={p.whole ? 'muted' : undefined}>{coverageText(p)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {points.length > 1 && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <span className="spark" role="img" aria-label={t('Duration of the last {n} succeeded runs, oldest first', { n: points.length })}>
            {points.map((p) => (
              <i key={p.runId} className={p.recent ? 'recent' : undefined} style={{ height: `${Math.max(4, (p.seconds / max) * 100)}%` }}
                title={`${shortDay(p.startedAt, tz)} ${hhmm(p.startedAt, tz)} · ${duration(p.seconds)}`} />
            ))}
          </span>
          <span className="muted" style={{ fontSize: 12 }}>
            {t('Duration of the last {n} succeeded runs, oldest first; the last 7 days in colour.', { n: points.length })}
          </span>
        </div>
      )}
      <span className="muted" style={{ fontSize: 12 }}>
        {t('p50 and p95 are nearest-rank percentiles of the run durations. Failed runs, runs whose outcome is unknown and runs still going are not counted, nor are runs OpsTide did not see while it was not watching. Marked from {ratio}× the baseline p50, with at least {n} runs in each period.', { ratio: d.longerAt.toLocaleString(locale()), n: d.minRuns })}
      </span>
    </section>
  )
}
