import type { ExplorerContext, CoverageScope } from '../explorer'
import { hhmm, hhmmss, isoDate } from '../time'

// What OpsTide was and was not watching over the range, so a quiet stretch is never mistaken for a calm one.

const stateWords: Record<CoverageScope['state'], string> = {
  watching: 'watched throughout', gap: 'has gaps', unavailable: 'cannot be read', disabled: 'observation off', before_first_observation: 'not yet watched',
}

function when(t: string, tz: string, from: string, to: string): string {
  const span = Date.parse(to) - Date.parse(from)
  return span > 36 * 3600e3 ? `${isoDate(t, tz).slice(5)} ${hhmm(t, tz)}` : hhmmss(t, tz)
}

export function Coverage({ ctx, tz }: { ctx: ExplorerContext; tz: string }) {
  const a = Date.parse(ctx.from)
  const b = Date.parse(ctx.to)
  const pct = (t: string) => `${Math.min(100, Math.max(0, ((Date.parse(t) - a) / (b - a)) * 100))}%`
  const width = (x: string, y: string) => `${Math.max(0.3, ((Math.min(Date.parse(y), b) - Math.max(Date.parse(x), a)) / (b - a)) * 100)}%`
  const trouble = ctx.scopes.filter((s) => s.state !== 'watching')
  return (
    <section aria-label="Coverage" className="cov">
      <div className="cov-bands" role="img"
        aria-label={ctx.bands.map((x) => `${x.kind.replace(/_/g, ' ')} ${hhmm(x.from, tz)} to ${hhmm(x.to, tz)}`).join('; ')}>
        {ctx.bands.map((x, i) => (
          <i key={i} className={`band ${x.kind}`} style={{ left: pct(x.from), width: width(x.from, x.to) }}
            title={x.kind === 'observable' ? 'Within the history OpsTide keeps and after it first observed the cluster' : x.kind === 'outside_retention' ? 'Older than the history OpsTide keeps' : 'Before OpsTide first observed the cluster'} />
        ))}
      </div>
      <p className="muted cov-line">
        {ctx.firstObservedAt ? <>First observed <span className="mono">{when(ctx.firstObservedAt, tz, ctx.from, ctx.to)}</span> · </> : 'OpsTide has not observed this cluster yet · '}
        history kept {ctx.retention.days > 0 ? `${ctx.retention.days} days` : 'without an age limit'}
        {trouble.length > 0 ? <> · <b>{trouble.length} of {ctx.scopes.length}</b> watches not whole in this range</> : ctx.scopes.length > 0 ? ' · every watch whole in this range' : ''}
      </p>
      {ctx.scopes.length > 0 && (
        <details>
          <summary className="label" style={{ cursor: 'pointer' }}>Coverage by watch</summary>
          <div className="cov-lanes">
            {ctx.scopes.map((s) => (
              <div key={s.scope} className="cov-lane">
                <span className="cov-name">{s.label}{s.namespace ? <span className="muted"> · {s.namespace}</span> : null}</span>
                <span className="cov-track" aria-label={`${s.label}: ${stateWords[s.state]}`}>
                  {s.watching.map((w, i) => <i key={i} className="on" style={{ left: pct(w.from), width: width(w.from, w.to) }} />)}
                  {s.gaps.map((g, i) => <i key={`g${i}`} className="gap" style={{ left: pct(g.from), width: width(g.from, g.to) }} title={`Not watched ${hhmmss(g.from, tz)} to ${hhmmss(g.to, tz)}`} />)}
                </span>
                <span className={`cov-state${s.state === 'watching' ? ' muted' : ''}`}>
                  {stateWords[s.state]}
                  {s.state === 'disabled' && s.lastObservedAt ? <> · last observed <span className="mono">{when(s.lastObservedAt, tz, ctx.from, ctx.to)}</span></> : null}
                  {s.state === 'disabled' && !s.lastObservedAt ? ' · never observed' : null}
                  {s.problem ? <> · {s.problem}</> : null}
                </span>
              </div>
            ))}
          </div>
          <p className="cov-legend muted">
            <span><i className="sw on" /> watched</span> <span><i className="sw gap" /> not watched: records may be missing</span>
            <span><i className="sw outside_retention" /> older than the history kept</span> <span><i className="sw before_first_observation" /> before OpsTide first observed</span>
          </p>
        </details>
      )}
    </section>
  )
}
