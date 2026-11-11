import type { Mark, Span } from './api'
import { t as tr } from './i18n'
import { hhmm, offsetMs, shortDay } from './time'

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

const STEPS = [5, 10, 15, 30, 60, 180, 360, 720, 1440].map((m) => m * 60_000)

/** The smallest usual step (5 minutes … a day) that gives about eight ticks. */
export function pickStep(spanMs: number): number {
  return STEPS.find((x) => x >= spanMs / 8) ?? STEPS[STEPS.length - 1]
}

export interface Tick { ms: number; label: string; midnight: boolean }

/**
 * Ticks for a window, on whole steps of the reader's local clock. Over more than a day the
 * ticks at midnight carry the day ("Wed 4") instead of a time.
 */
export function tickList(from: string, to: string, tz: string): Tick[] {
  const f = Date.parse(from)
  const t = Date.parse(to)
  const step = pickStep(t - f)
  const multiDay = t - f > 25 * 3600_000
  const off = offsetMs(tz, new Date(f))
  const out: Tick[] = []
  for (let x = Math.ceil((f + off) / step) * step - off; x <= t; x += step) {
    const midnight = (x + offsetMs(tz, new Date(x))) % 86_400_000 === 0
    out.push({ ms: x, midnight, label: midnight && multiDay ? shortDay(x, tz) : hhmm(x, tz) })
  }
  return out
}

/** Words for a mark, for its tooltip and screen readers. */
export function markLabel(m: Mark, time: (t: string) => string, dur: (s: number) => string): string {
  const at = time(m.start)
  const took = m.end ? dur((Date.parse(m.end) - Date.parse(m.start)) / 1000) : ''
  const slow = m.slow ? tr(', longer than expected') : ''
  const manual = m.manual ? tr(' (started by hand)') : ''
  switch (m.state) {
    case 'succeeded':
      return `${took ? tr('{at} succeeded, {took}', { at, took }) : tr('{at} succeeded', { at })}${slow}${manual}`
    case 'failed':
      return `${took ? tr('{at} failed after {took}', { at, took }) : tr('{at} failed', { at })}${slow}${manual}`
    case 'running':
      return `${took ? tr('{at} running, {took} so far', { at, took }) : tr('{at} running', { at })}${slow}${manual}`
    case 'pending':
      return `${tr('{at} created, not started yet', { at })}${manual}`
    case 'unknown':
      return tr('{at} outcome unknown: removed before it finished', { at })
    case 'expected':
      return m.count && m.count > 1 ? tr('{n} runs expected from {at}', { n: m.count, at }) : tr('{at} expected', { at })
    case 'due':
      return tr('{at} due, not seen yet', { at })
    case 'missed':
      return tr('{at} did not start', { at })
    case 'unconfirmed':
      return tr('{at} not confirmed: collection gap', { at })
    case 'skipped':
      return tr('{at} skipped: previous run still active', { at })
    case 'suspended':
      return tr('{at} suspended', { at })
  }
}

export function inGap(t: string, gaps: Span[]): boolean {
  const x = Date.parse(t)
  return gaps.some((g) => x >= Date.parse(g.from) && x < Date.parse(g.to))
}
