import type { CSSProperties } from 'react'
import type { Mark, Span } from '../api'
import { markLabel, place, ticks } from '../geometry'
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
}

/**
 * Layout A: one lane per row across the day. Bar width is the run's
 * duration; gaps and the now line cut across every lane.
 */
export function Timeline({ label, from, to, tz, now, gaps, lanes, markHref, compact, labelWidth = 260, nowPerLane }: Props) {
  const hours = ticks(from, to, 3)
  const nowPos = now && !nowPerLane ? place(now, undefined, from, to) : null
  const style = { '--label': `${labelWidth}px` } as CSSProperties
  return (
    <div className="tl" style={style}>
      <div className="tl-head">
        <span className="label" style={{ paddingBottom: 6 }}>{label}</span>
        <div className="tl-hours mono" aria-hidden="true">
          {hours.map((h, i) => {
            const left = ((h - Date.parse(from)) / (Date.parse(to) - Date.parse(from))) * 100
            const last = i === hours.length - 1 && h === Date.parse(to)
            return <span key={h} style={{ left: `${left}%` }}>{last ? '24:00' : hhmm(h, tz)}</span>
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
                {laneNow && <span className="tl-now" style={{ left: `${laneNow.left}%` }} />}
              </div>
            </div>
          )
        })}
        {!nowPerLane &&
          gaps.map((g) => {
            const p = place(g.from, g.to, from, to)
            return (
              p && (
                <span key={g.from} className="tl-gap" title={`Not watching ${hhmm(g.from, tz)}–${hhmm(g.to, tz)}`}
                  style={{ left: `calc(var(--label) + (100% - var(--label)) * ${p.left / 100})`, width: `max(4px, calc((100% - var(--label)) * ${p.width / 100}))` }} />
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

function LaneName({ lane }: { lane: TimelineLane }) {
  return (
    <>
      <span style={lane.emphasis ? { fontWeight: 600 } : undefined}>{lane.name}</span>
      {lane.meta && <span className="meta mono">{lane.meta}</span>}
    </>
  )
}

export function Legend({ note, items }: { note?: string; items?: ('succeeded' | 'failed' | 'running' | 'expected' | 'missed' | 'unconfirmed' | 'gap' | 'slow')[] }) {
  const all = items ?? ['succeeded', 'failed', 'running', 'expected', 'missed', 'unconfirmed', 'gap']
  const text: Record<string, string> = {
    succeeded: 'succeeded', failed: 'failed', running: 'running', expected: 'expected, from the schedule',
    missed: 'did not start', unconfirmed: 'not confirmed', gap: 'collection gap', slow: 'longer than expected',
  }
  return (
    <div className="legend">
      {all.map((k) => (
        <span key={k}>
          {k === 'gap' ? (
            <i style={{ background: 'repeating-linear-gradient(135deg, var(--hatch) 0 2px, transparent 2px 5px)' }} />
          ) : k === 'slow' ? (
            <i className="mark succeeded slow" />
          ) : (
            <i className={`mark ${k}`} />
          )}
          {text[k]}
        </span>
      ))}
      {note && <span>{note}</span>}
    </div>
  )
}
