import type { CSSProperties } from 'react'
import type { Bucket, Mark, Span } from '../api'
import { markLabel, place, tickList } from '../geometry'
import { Link } from '../router'
import { duration, hhmm } from '../time'

export interface TimelineLane {
  key: string
  name: string
  meta?: string
  href?: string
  emphasis?: boolean
  from?: string // a lane may have its own day (Operation detail)
  to?: string
  marks: Mark[]
  /** Runs per bucket, for a lane too busy to draw each run. */
  buckets?: Bucket[]
  bucketMs?: number
  /** Overrides the words for this lane's marks. */
  markTitle?: (m: Mark) => string
}

interface Props {
  label: string
  from: string
  to: string
  tz: string
  now?: string
  gaps: Span[]
  lanes: TimelineLane[]
  markHref?: (m: Mark, lane: TimelineLane) => string | undefined
  compact?: boolean
  labelWidth?: number
  /** Show "now" only on lanes whose own day contains it. */
  nowPerLane?: boolean
  /** When OpsTide first watched: the time before it is drawn as "not watching yet", not as a gap. */
  watchedSince?: string
}

/**
 * Layout A: one lane per row across the day. Bar width is the run's
 * duration; gaps and the now line cut across every lane.
 */
export function Timeline({ label, from, to, tz, now, gaps, lanes, markHref, compact, labelWidth = 260, nowPerLane, watchedSince }: Props) {
  const hours = tickList(from, to, tz)
  const nowPos = now && !nowPerLane ? place(now, undefined, from, to) : null
  const style = { '--label': `${labelWidth}px` } as CSSProperties
  return (
    <div className="tl" style={style}>
      <div className="tl-head">
        <span className="label" style={{ paddingBottom: 6 }}>{label}</span>
        <div className="tl-hours mono" aria-hidden="true">
          {hours.map((h) => {
            const left = ((h.ms - Date.parse(from)) / (Date.parse(to) - Date.parse(from))) * 100
            // Keep the first and last label inside the chart.
            const shift = left < 2 ? '0' : left > 98 ? '-100%' : '-50%'
            return (
              <span key={h.ms} style={{ left: `${left}%`, transform: `translateX(${shift})`, color: h.midnight ? 'var(--ink)' : undefined }}>{h.label}</span>
            )
          })}
        </div>
      </div>
      <div className="tl-body">
        {lanes.map((lane) => {
          const lf = lane.from ?? from
          const lt = lane.to ?? to
          const laneNow = nowPerLane && now ? place(now, undefined, lf, lt) : null
          const laneGaps = nowPerLane ? gaps : []
          return (
            <div key={lane.key} className={`tl-lane${compact ? ' compact' : ''}`}>
              {lane.href ? (
                <Link className="tl-name" href={lane.href}><LaneName lane={lane} /></Link>
              ) : (
                <span className="tl-name"><LaneName lane={lane} /></span>
              )}
              <div className="tl-track">
                {laneGaps.map((g) => {
                  const p = place(g.from, g.to, lf, lt)
                  return p && <span key={g.from} className="tl-gap" style={{ left: `${p.left}%`, width: `${p.width}%` }} />
                })}
                {lane.marks.map((m, i) => {
                  const p = place(m.start, m.end, lf, lt)
                  if (!p) return null
                  const text = lane.markTitle?.(m) ?? markLabel(m, (t) => hhmm(t, tz), duration)
                  const cls = `mark ${m.state}${m.slow ? ' slow' : ''}`
                  const st = { left: `${p.left}%`, width: `${p.width}%` }
                  const href = markHref?.(m, lane)
                  return href ? (
                    <Link key={i} href={href} className={cls} style={st} title={text} aria-label={`${lane.name}: ${text}`} />
                  ) : (
                    <span key={i} className={cls} style={st} title={text} />
                  )
                })}
                {lane.buckets && <HourBars lane={lane} from={lf} to={lt} tz={tz} />}
                {laneNow && <span className="tl-now" style={{ left: `${laneNow.left}%` }} />}
              </div>
            </div>
          )
        })}
        {!nowPerLane && watchedSince && Date.parse(watchedSince) > Date.parse(from) && (() => {
          const p = place(from, watchedSince, from, to)
          return p && (
            <span className="tl-before" title={`OpsTide was not watching yet; it started at ${hhmm(watchedSince, tz)}`}
              style={{ left: 'var(--label)', width: `calc((100% - var(--label)) * ${p.width / 100})` }} />
          )
        })()}
        {!nowPerLane &&
          gaps.map((g) => {
            const p = place(g.from, g.to, from, to)
            return (
              p && (
                <span key={g.from} className="tl-gap" title={`Not watching ${hhmm(g.from, tz)}–${hhmm(g.to, tz)}`}
                  style={{ left: `calc(var(--label) + (100% - var(--label)) * ${p.left / 100})`, width: `max(4px, calc((100% - var(--label)) * ${p.width / 100}))` }}>
                  <span className="tl-gaplabel mono">not watching</span>
                </span>
              )
            )
          })}
        {nowPos && (
          <>
            <span className="tl-now" style={{ left: `calc(var(--label) + (100% - var(--label)) * ${nowPos.left / 100})` }} />
            <span className="tl-nowlabel mono" style={{ left: `calc(var(--label) + (100% - var(--label)) * ${nowPos.left / 100})` }}>
              now {hhmm(now!, tz)}
            </span>
          </>
        )}
      </div>
    </div>
  )
}

/** One bar per bucket: how many runs, with the failed ones on top in red. */
function HourBars({ lane, from, to, tz }: { lane: TimelineLane; from: string; to: string; tz: string }) {
  const buckets = lane.buckets ?? []
  const ms = lane.bucketMs ?? 3600_000
  const total = (b: Bucket) => b.runs + (b.missed ?? 0) + (b.skipped ?? 0)
  const max = Math.max(1, ...buckets.map(total))
  const px = (n: number) => (n / max) * 34
  return (
    <>
      {buckets.map((b) => {
        const end = new Date(Date.parse(b.start) + ms).toISOString()
        const p = place(b.start, end, from, to)
        if (!p) return null
        const ok = px(b.runs)
        const failed = px(b.failed ?? 0)
        const missed = px(b.missed ?? 0)
        const skipped = px(b.skipped ?? 0)
        const title = [
          `${hhmm(b.start, tz)} · ${b.runs} runs`,
          b.failed ? `${b.failed} failed` : '',
          b.unknown ? `${b.unknown} outcome unknown` : '',
          b.missed ? `${b.missed} did not start` : '',
          b.skipped ? `${b.skipped} skipped` : '',
        ].filter(Boolean).join(', ')
        const style = { left: `${p.left}%`, width: `calc(${p.width}% - 1px)` }
        return (
          <span key={b.start}>
            {ok > 0 && <span className="hbar" title={title} style={{ ...style, height: ok }} />}
            {failed > 0 && <span className="hbar f" title={title} style={{ ...style, height: failed, bottom: 4 + ok - failed }} />}
            {missed > 0 && <span className="hbar m" title={title} style={{ ...style, height: missed, bottom: 4 + ok }} />}
            {skipped > 0 && <span className="hbar s" title={title} style={{ ...style, height: skipped, bottom: 4 + ok + missed }} />}
          </span>
        )
      })}
    </>
  )
}

function LaneName({ lane }: { lane: TimelineLane }) {
  return (
    <>
      <span style={lane.emphasis ? { fontWeight: 600 } : undefined}>{lane.name}</span>
      {lane.meta && <span className="meta mono">{lane.meta}</span>}
    </>
  )
}

export function Legend({ note, items, before }: { note?: string; items?: ('succeeded' | 'failed' | 'running' | 'expected' | 'missed' | 'gap' | 'slow')[]; before?: boolean }) {
  const all = items ?? ['succeeded', 'failed', 'running', 'expected', 'missed', 'gap']
  const text: Record<string, string> = {
    succeeded: 'succeeded', failed: 'failed', running: 'running', expected: 'expected, from the schedule',
    missed: 'did not start', gap: 'not watching: nothing is claimed about this time', slow: 'longer than expected',
  }
  return (
    <div className="legend">
      {all.map((k) => (
        <span key={k}>
          {k === 'gap' ? (
            <i style={{ width: 14, background: 'rgba(142,151,156,.2)', borderLeft: '1px dashed var(--hatch)', borderRight: '1px dashed var(--hatch)' }} />
          ) : k === 'slow' ? (
            <i className="mark succeeded slow" />
          ) : (
            <i className={`mark ${k}`} />
          )}
          {text[k]}
        </span>
      ))}
      {before && (
        <span>
          <i style={{ background: 'repeating-linear-gradient(135deg, var(--line) 0 2px, transparent 2px 6px)' }} />OpsTide was not watching yet
        </span>
      )}
      {note && <span>{note}</span>}
    </div>
  )
}
