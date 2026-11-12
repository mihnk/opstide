import type { ExplorerContext, CoverageScope } from '../explorer'
import { hhmm, hhmmss, isoDate } from '../time'
import { t } from '../i18n'

// What OpsTide was and was not watching over the range, so a quiet stretch is never mistaken for a calm one.

const stateWords: Record<CoverageScope['state'], string> = {
  watching: 'watched throughout', gap: 'has gaps', unavailable: 'cannot be read', disabled: 'observation off', before_first_observation: 'not yet watched',
}

function when(t: string, tz: string, from: string, to: string): string {
  const span = Date.parse(to) - Date.parse(from)
  return span > 36 * 3600e3 ? `${isoDate(t, tz).slice(5)} ${hhmm(t, tz)}` : hhmmss(t, tz)
}

export function Coverage({ ctx, tz, compact }: { ctx: ExplorerContext; tz: string; compact?: boolean }) {
  const a = Date.parse(ctx.from)
  const b = Date.parse(ctx.to)
  const pct = (t: string) => `${Math.min(100, Math.max(0, ((Date.parse(t) - a) / (b - a)) * 100))}%`
  const width = (x: string, y: string) => `${Math.max(0.3, ((Math.min(Date.parse(y), b) - Math.max(Date.parse(x), a)) / (b - a)) * 100)}%`
  const trouble = ctx.scopes.filter((s) => s.state !== 'watching')
  return (
    <section aria-label={t('Coverage')} className={`cov${compact ? ' compact' : ''}`}>
      {!compact && <div className="cov-bands" role="img"
        aria-label={ctx.bands.map((x) => t('{kind} {from} to {to}', { kind: t(x.kind.replace(/_/g, ' ')), from: hhmm(x.from, tz), to: hhmm(x.to, tz) })).join('; ')}>
        {ctx.bands.map((x, i) => (
          <i key={i} className={`band ${x.kind}`} style={{ left: pct(x.from), width: width(x.from, x.to) }}
            title={x.kind === 'observable' ? t('Within the history OpsTide keeps and after it first observed the cluster') : x.kind === 'outside_retention' ? t('Older than the history OpsTide keeps') : t('Before OpsTide first observed the cluster')} />
        ))}
      </div>}
      <p className="muted cov-line">
        {ctx.firstObservedAt ? <>{t('First observed')} <span className="mono">{when(ctx.firstObservedAt, tz, ctx.from, ctx.to)}</span> · </> : `${t('OpsTide has not observed this cluster yet')} · `}
        {ctx.retention.days > 0 ? t('history kept {n} days', { n: ctx.retention.days }) : t('history kept without an age limit')}
        {trouble.length > 0 ? <> · <b>{t('{n} of {m}', { n: trouble.length, m: ctx.scopes.length })}</b> {t('watches not whole in this range')}</> : ctx.scopes.length > 0 ? ` · ${t('every watch whole in this range')}` : ''}
      </p>
      {ctx.retention.sources.some((s) => s.cappedTotal > 0) && (
        <p className="muted cov-line">
          {t('Cut short by a size budget, not by age:')} {ctx.retention.sources.filter((s) => s.cappedTotal > 0).map((s) => `${t(s.label)} (${s.retainedSince ? t('{n} removed, now from {when}', { n: s.cappedTotal.toLocaleString('en'), when: `${isoDate(s.retainedSince, tz).slice(5)} ${hhmm(s.retainedSince, tz)}` }) : t('{n} removed', { n: s.cappedTotal.toLocaleString('en') })})`).join('; ')}.
        </p>
      )}
      {ctx.scopes.length > 0 && (
        <details>
          <summary className="label" style={{ cursor: 'pointer' }}>{t('Coverage by watch')}</summary>
          <div className="cov-lanes">
            {ctx.scopes.map((s) => (
              <div key={s.scope} className="cov-lane">
                <span className="cov-name">{t(s.label)}{s.namespace ? <span className="muted"> · {s.namespace}</span> : null}</span>
                <span className="cov-track" aria-label={`${t(s.label)}: ${t(stateWords[s.state])}`}>
                  {s.watching.map((w, i) => <i key={i} className="on" style={{ left: pct(w.from), width: width(w.from, w.to) }} />)}
                  {s.gaps.map((g, i) => <i key={`g${i}`} className="gap" style={{ left: pct(g.from), width: width(g.from, g.to) }} title={t('Not watched {from} to {to}', { from: hhmmss(g.from, tz), to: hhmmss(g.to, tz) })} />)}
                </span>
                <span className={`cov-state${s.state === 'watching' ? ' muted' : ''}`}>
                  {t(stateWords[s.state])}
                  {s.state === 'disabled' && s.lastObservedAt ? <> · {t('last observed')} <span className="mono">{when(s.lastObservedAt, tz, ctx.from, ctx.to)}</span></> : null}
                  {s.state === 'disabled' && !s.lastObservedAt ? ` · ${t('never observed')}` : null}
                  {s.problem ? <> · {s.problem}</> : null}
                </span>
              </div>
            ))}
          </div>
          <p className="cov-legend muted">
            <span><i className="sw on" /> {t('watched')}</span> <span><i className="sw gap" /> {t('not watched: records may be missing')}</span>
            <span><i className="sw outside_retention" /> {t('older than the history kept')}</span> <span><i className="sw before_first_observation" /> {t('before OpsTide first observed')}</span>
          </p>
        </details>
      )}
    </section>
  )
}
