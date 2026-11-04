import { describe, expect, it } from 'vitest'
import type { Day } from './api'
import { daySentence, exceptionText, joinAnd } from './narrative'
import { place, ticks } from './geometry'
import { duration, hhmm, isoDate, longDay, offset, relativeTime, shiftDate } from './time'

const tz = 'Europe/Istanbul'
const at = (h: number, m: number) => new Date(Date.UTC(2026, 10, 4, h - 3, m)).toISOString()

function day(over: Partial<Day['counts']> = {}): Day {
  return {
    schema: 1, cluster: 'c', date: '2026-11-04', timeZone: tz, from: at(0, 0), to: at(24, 0), now: at(10, 6),
    counts: { observed: 24, succeeded: 22, failed: 1, running: 1, unknown: 0, unconfirmed: 1, missed: 0, skipped: 0, stillDue: 30, ...over },
    gaps: [{ from: at(3, 10), to: at(3, 14) }], lanes: [], idle: 0, rollouts: [], exceptions: [], upcoming: [],
  }
}

describe('time', () => {
  it('formats in the chosen zone', () => {
    expect(hhmm(at(8, 15), tz)).toBe('08:15')
    expect(hhmm(at(8, 15), 'UTC')).toBe('05:15')
    expect(longDay(at(10, 0), tz)).toBe('Wednesday 4 November')
    expect(isoDate(at(1, 0), tz)).toBe('2026-11-04')
    expect(isoDate(at(1, 0), 'UTC')).toBe('2026-11-03')
    expect(offset(tz, new Date(at(10, 0)))).toBe('UTC+03')
    expect(offset('UTC')).toBe('UTC')
    expect(shiftDate('2026-03-01', -1)).toBe('2026-02-28')
  })
  it('says how long and when', () => {
    expect(duration(937)).toBe('15m 37s')
    expect(duration(40)).toBe('40 s')
    expect(duration(7500)).toBe('2h 05m')
    expect(relativeTime(at(8, 0), at(10, 6), tz)).toBe('today 08:00')
    expect(relativeTime(at(8, 0).replace('-04T', '-03T'), at(10, 6), tz)).toBe('Tue 08:00')
  })
})

describe('narrative', () => {
  it('tells the day as the design does', () => {
    expect(daySentence(day(), true)).toBe(
      'By 10:06 OpsTide saw 24 runs: 22 succeeded, 1 failed and 1 is still running. ' +
        'One expected run could not be confirmed: collection paused 03:10–03:14. 30 more runs are expected before midnight.',
    )
  })
  it('handles a quiet past day', () => {
    expect(daySentence(day({ observed: 0, succeeded: 0, failed: 0, running: 0, unconfirmed: 0, stillDue: 0 }), false)).toBe('OpsTide saw no runs.')
  })
  it('joins with and', () => {
    expect(joinAnd(['a', 'b', 'c'])).toBe('a, b and c')
    expect(joinAnd(['a'])).toBe('a')
  })
  it('explains a failure with its numbers', () => {
    const t = exceptionText({ at: at(8, 15), kind: 'failed', operationId: 'x', name: 'Payment reconciliation', scheduledAt: at(8, 0),
      seconds: 937, expectedSeconds: 240, attempts: 2, reason: 'BackoffLimitExceeded', owner: 'team-payments', runbookUrl: 'https://x' }, tz)
    expect(t.title).toBe('Payment reconciliation failed')
    expect(t.detail).toBe('The 08:00 run failed after 2 attempts (BackoffLimitExceeded). It took 15m 37s against an expected 4m 00s. Runbook linked · team-payments.')
  })
})

describe('geometry', () => {
  it('places marks on the day and clips at its edges', () => {
    expect(place(at(12, 0), at(18, 0), at(0, 0), at(24, 0))).toEqual({ left: 50, width: 25 })
    expect(place(at(23, 0), at(26, 0), at(0, 0), at(24, 0))?.width).toBeCloseTo(100 / 24)
    expect(place(at(25, 0), undefined, at(0, 0), at(24, 0))).toBeNull()
    expect(place(at(6, 0), undefined, at(0, 0), at(24, 0))).toEqual({ left: 25, width: 0 })
  })
  it('ticks every three hours', () => {
    expect(ticks(at(0, 0), at(24, 0), 3)).toHaveLength(9)
  })
})
