import type { Mark, Span } from './api'

/** Position of [start, end) on a timeline from..to, in percent. */
export function place(start: string, end: string | undefined, from: string, to: string): { left: number; width: number } | null {
  const f = Date.parse(from)
  const t = Date.parse(to)
  const s = Date.parse(start)
  const e = end ? Date.parse(end) : s
  if (!(t > f) || e < f || s >= t) return null
  const a = Math.max(s, f)
  const b = Math.min(Math.max(e, s), t)
  return { left: ((a - f) / (t - f)) * 100, width: ((b - a) / (t - f)) * 100 }
}

/** Hour ticks every step hours from..to, as epoch ms. */
export function ticks(from: string, to: string, stepHours: number): number[] {
  const out: number[] = []
  const f = Date.parse(from)
  const t = Date.parse(to)
  for (let x = f; x <= t; x += stepHours * 3600e3) out.push(x)
  return out
}

/** Words for a mark, for its tooltip and screen readers. */
export function markLabel(m: Mark, time: (t: string) => string, dur: (s: number) => string): string {
  const at = time(m.start)
  const took = m.end ? dur((Date.parse(m.end) - Date.parse(m.start)) / 1000) : ''
  const slow = m.slow ? ', longer than expected' : ''
  const manual = m.manual ? ' (started by hand)' : ''
  switch (m.state) {
    case 'succeeded':
      return `${at} succeeded${took ? `, ${took}` : ''}${slow}${manual}`
    case 'failed':
      return `${at} failed${took ? ` after ${took}` : ''}${slow}${manual}`
    case 'running':
      return `${at} running${took ? `, ${took} so far` : ''}${slow}${manual}`
    case 'pending':
      return `${at} created, not started yet${manual}`
    case 'unknown':
      return `${at} outcome unknown: removed before it finished`
    case 'expected':
      return m.count && m.count > 1 ? `${m.count} runs expected from ${at}` : `${at} expected`
    case 'due':
      return `${at} due, not seen yet`
    case 'missed':
      return `${at} did not start`
    case 'unconfirmed':
      return `${at} not confirmed: collection gap`
    case 'skipped':
      return `${at} skipped: previous run still active`
    case 'suspended':
      return `${at} suspended`
  }
}

export function inGap(t: string, gaps: Span[]): boolean {
  const x = Date.parse(t)
  return gaps.some((g) => x >= Date.parse(g.from) && x < Date.parse(g.to))
}
