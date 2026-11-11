import { describe, expect, it } from 'vitest'
import type { Range } from './api'
import { changeText, runTime } from './components/Duration'
import { noteWords } from './screens/Images'
import { setLangForTest } from './i18n'
import { exceptionText, joinAnd, rangeSentence } from './narrative'
import { pickStep, place, tickList } from './geometry'
import { duration, hhmm, isoDate, longDay, offset, relativeTime, shiftDate, utcToZoned, zonedToUtc } from './time'
import { centreWindow, DAY, HOUR, lengthWords, MAX_WINDOW, MIN_WINDOW, MINUTE, moveWindow, params, presetSelection, pressedPreset, rangeQuery, rangeWords, resizeWindow, selection, selectionFor, spanWords, title } from './window'

const tz = 'Europe/Istanbul'
const at = (h: number, m: number) => new Date(Date.UTC(2026, 10, 4, h - 3, m)).toISOString()

function range(over: Partial<Range['counts']> = {}): Range {
  return {
    schema: 1, cluster: 'c', timeZone: tz, from: at(0, 0), to: at(24, 0), now: at(10, 6), bucketSeconds: 3600,
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

describe('zoned input', () => {
  it('reads wall-clock time in the chosen zone', () => {
    expect(new Date(zonedToUtc('2026-11-04T08:51', 'Europe/Istanbul')).toISOString()).toBe('2026-11-04T05:51:00.000Z')
    expect(new Date(zonedToUtc('2026-11-04T08:51', 'UTC')).toISOString()).toBe('2026-11-04T08:51:00.000Z')
    expect(utcToZoned(Date.parse('2026-11-04T05:51:00Z'), 'Europe/Istanbul')).toBe('2026-11-04T08:51')
    // Berlin leaves summer time on 25 October 2026: 01:30 UTC is 02:30 CET, 00:30 UTC is 02:30 CEST.
    expect(new Date(zonedToUtc('2026-10-25T12:00', 'Europe/Berlin')).toISOString()).toBe('2026-10-25T11:00:00.000Z')
    expect(new Date(zonedToUtc('2026-10-24T12:00', 'Europe/Berlin')).toISOString()).toBe('2026-10-24T10:00:00.000Z')
    expect(zonedToUtc('soon', 'UTC')).toBeNaN()
  })
})

describe('narrative', () => {
  it('tells the window as the design does', () => {
    expect(rangeSentence(range(), tz, true)).toBe(
      'By 10:06 OpsTide saw 24 runs: 22 succeeded, 1 failed and 1 is still running. ' +
        'It was not watching 03:10–03:14, so 1 expected run in that time is not confirmed. 30 more runs are expected before midnight.',
    )
  })
  it('handles a quiet window and says when watching began', () => {
    const quiet = range({ observed: 0, succeeded: 0, failed: 0, running: 0, unconfirmed: 0, stillDue: 0 })
    quiet.gaps = []
    expect(rangeSentence(quiet, tz, false)).toBe('In this window OpsTide saw no runs.')
    const fresh = range()
    fresh.gaps = []
    fresh.watchedSince = at(9, 30)
    expect(rangeSentence(fresh, tz, true)).toContain('OpsTide started watching at 09:30; before that it says nothing.')
  })
  it('counts several gaps as one thing', () => {
    const d = range()
    d.gaps = [{ from: at(3, 10), to: at(3, 14) }, { from: at(6, 0), to: at(7, 30) }]
    expect(rangeSentence(d, tz, true)).toContain('It was not watching 2 stretches, 1 h 34 min in all')
  })
  it('joins with and', () => {
    expect(joinAnd(['a', 'b', 'c'])).toBe('a, b and c')
    expect(joinAnd(['a'])).toBe('a')
  })
  it('groups exceptions: a streak, a gap, a long run', () => {
    const streak = exceptionText({ kind: 'failing', at: at(9, 16), from: at(8, 0), to: at(9, 16), count: 38, name: 'Payments', attempts: 2, reason: 'BackoffLimitExceeded', owner: 'team-payments', runbookUrl: 'https://x' }, tz, at(0, 0))
    expect(streak).toEqual({ when: '08:00–09:16', title: 'Payments: every run failed for 1 h 16 min', detail: '38 runs in a row ended after 2 attempts (BackoffLimitExceeded). Runbook linked · team-payments.' })
    const gap = exceptionText({ kind: 'gap', at: at(10, 12), from: at(10, 12), to: at(10, 30), count: 1, operations: 5, expectedRuns: 32 }, tz, at(0, 0))
    expect(gap.title).toBe('Not watching for 18 min')
    expect(gap.detail).toBe('5 operations, 32 expected runs fell inside it. They are not confirmed, and OpsTide does not call them missed.')
    const one = exceptionText({ kind: 'failed', at: at(8, 15), count: 1, name: 'Payments', scheduledAt: at(8, 0), seconds: 937, expectedSeconds: 240, attempts: 2, reason: 'BackoffLimitExceeded' }, tz, at(0, 0))
    expect(one.detail).toBe('The 08:00 run failed after 2 attempts (BackoffLimitExceeded). It took 15m 37s against an expected 4m 00s.')
    expect(exceptionText({ kind: 'slow', at: at(11, 48), count: 3, name: 'Sync', seconds: 81, expectedSeconds: 40 }, tz, at(0, 0)).title).toBe('Sync ran long 3 times')
    // Over several days a time says which day.
    expect(exceptionText({ kind: 'failed', at: at(11, 40), count: 2, name: 'P' }, tz, at(-30, 0), at(11, 51)).when).toBe('Wed 4 11:40')
  })
})

describe('geometry', () => {
  it('places marks on the day and clips at its edges', () => {
    expect(place(at(12, 0), at(18, 0), at(0, 0), at(24, 0))).toEqual({ left: 50, width: 25 })
    expect(place(at(23, 0), at(26, 0), at(0, 0), at(24, 0))?.width).toBeCloseTo(100 / 24)
    expect(place(at(25, 0), undefined, at(0, 0), at(24, 0))).toBeNull()
    expect(place(at(6, 0), undefined, at(0, 0), at(24, 0))).toEqual({ left: 25, width: 0 })
  })
  it('picks ticks that fit the window, on the reader\'s own clock', () => {
    expect(pickStep(3 * 3600_000)).toBe(30 * 60_000)
    expect(pickStep(24 * 3600_000)).toBe(3 * 3600_000)
    expect(pickStep(36 * 3600_000)).toBe(6 * 3600_000)
    expect(pickStep(5 * 86_400_000)).toBe(86_400_000)
    const day = tickList(at(0, 0), at(24, 0), tz)
    expect(day.map((t) => t.label)).toEqual(['00:00', '03:00', '06:00', '09:00', '12:00', '15:00', '18:00', '21:00', '00:00'])
    // Over more than a day, midnight carries the day instead of a time.
    const two = tickList(at(0, 0), at(36, 0), tz)
    expect(two.filter((t) => t.midnight).map((t) => t.label)).toEqual(['Wed 4', 'Thu 5'])
    // A zone with a half-hour offset still gets whole local hours.
    expect(tickList('2026-11-04T08:20:00Z', '2026-11-04T11:20:00Z', 'Asia/Kolkata').map((t) => t.label)).toEqual(['14:00', '14:30', '15:00', '15:30', '16:00', '16:30'])
  })
})

describe('window', () => {
  const now = at(11, 51)
  const nowMs = Date.parse(now)
  it('reads the URL', () => {
    expect(selection(new URLSearchParams(''))).toEqual({ kind: 'today' })
    expect(selection(new URLSearchParams('range=3h'))).toEqual({ kind: 'range', range: '3h' })
    expect(selection(new URLSearchParams('range=soon'))).toEqual({ kind: 'today' })
    expect(selection(new URLSearchParams('date=2026-11-03'))).toEqual({ kind: 'date', date: '2026-11-03' })
    expect(selection(new URLSearchParams('from=2026-11-04T05:00:00Z&to=2026-11-04T08:00:00Z'))).toMatchObject({ kind: 'custom' })
    expect(selection(new URLSearchParams('from=yesterday&to=now'))).toEqual({ kind: 'today' })
    // Links that cannot be answered fall back to today instead of an error page.
    expect(selection(new URLSearchParams('range=0m'))).toEqual({ kind: 'today' })
    expect(selection(new URLSearchParams('from=2026-11-04T08:00:00Z&to=2026-11-04T07:00:00Z'))).toEqual({ kind: 'today' })
    expect(selection(new URLSearchParams('from=2026-11-04T08:00:00Z&to=2026-11-04T08:02:00Z'))).toEqual({ kind: 'today' })
  })
  it('asks the API for it', () => {
    expect(rangeQuery({ kind: 'today' }, 'UTC')).toBe('?tz=UTC')
    expect(rangeQuery({ kind: 'range', range: '5d' }, 'Europe/Istanbul')).toBe('?tz=Europe%2FIstanbul&within=5d')
    expect(rangeQuery({ kind: 'date', date: '2026-11-03' }, 'UTC')).toBe('?tz=UTC&date=2026-11-03')
  })
  it('knows Today and Yesterday as presets', () => {
    expect(presetSelection('yesterday', now, tz)).toEqual({ kind: 'date', date: '2026-11-03' })
    expect(pressedPreset({ kind: 'date', date: '2026-11-03' }, now, tz)).toBe('yesterday')
    expect(pressedPreset({ kind: 'date', date: '2026-10-30' }, now, tz)).toBeNull()
    expect(pressedPreset({ kind: 'range', range: '6h' }, now, tz)).toBe('6h')
    expect(pressedPreset({ kind: 'custom', from: now, to: now }, now, tz)).toBeNull()
  })
  it('titles the window so the menu never lies', () => {
    expect(title({ kind: 'today' }, at(0, 0), at(24, 0), tz, now).title).toBe('Today')
    expect(title({ kind: 'range', range: '3h' }, at(8, 51), now, tz, now)).toEqual({ title: 'Last 3 hours', sub: '08:51 – 11:51 · live' })
    expect(rangeWords('90m')).toBe('Last 90 minutes')
    expect(rangeWords('120m')).toBe('Last 2 hours')
    expect(rangeWords('1h')).toBe('Last hour')
    expect(rangeWords('5d')).toBe('Last 5 days')
    expect(spanWords(at(-3, 0), now, tz)).toBe('Tue 3 21:00 – Wed 4 11:51')
    expect(lengthWords(36 * HOUR)).toBe('36 h')
    expect(lengthWords(45 * MINUTE)).toBe('45 min')
  })
  it('moves and resizes inside the strip and the limits', () => {
    const lo = nowMs - 5 * DAY
    const hi = nowMs
    const w = { from: nowMs - 3 * HOUR, to: nowMs }
    expect(moveWindow(w, 2 * HOUR, lo, hi)).toEqual(w)
    expect(moveWindow(w, -DAY, lo, hi)).toEqual({ from: w.from - DAY, to: w.to - DAY })
    expect(moveWindow(w, -10 * DAY, lo, hi).from).toBe(lo)
    expect(resizeWindow(w, 'from', -10 * DAY, lo, hi).from).toBe(lo) // the strip ends first
    expect(resizeWindow(w, 'from', -10 * DAY, nowMs - 30 * DAY, hi).from).toBe(nowMs - MAX_WINDOW) // then the 7-day limit
    // A window already outside the bounds can be moved without snapping into them.
    const outsideWin = { from: lo - 2 * DAY, to: lo - DAY }
    expect(moveWindow(outsideWin, DAY / 2, Math.min(lo, outsideWin.from), Math.max(hi, outsideWin.to))).toEqual({ from: lo - 1.5 * DAY, to: lo - 0.5 * DAY })
    expect(resizeWindow(w, 'from', 10 * HOUR, lo, hi).from).toBe(w.to - MIN_WINDOW)
    expect(resizeWindow(w, 'to', -10 * HOUR, lo, hi).to).toBe(w.from + MIN_WINDOW)
    expect(resizeWindow(w, 'to', HOUR, lo, hi).to).toBe(hi)
    expect(centreWindow(w, lo, lo, hi).from).toBe(lo)
    expect(centreWindow(w, nowMs - DAY, lo, hi)).toEqual({ from: nowMs - DAY - 1.5 * HOUR, to: nowMs - DAY + 1.5 * HOUR })
  })
  it('follows now when the window ends there, and is fixed otherwise', () => {
    expect(selectionFor({ from: nowMs - 3 * HOUR, to: nowMs }, nowMs)).toEqual({ kind: 'range', range: '3h' })
    expect(selectionFor({ from: nowMs - 90 * MINUTE, to: nowMs - 30_000 }, nowMs)).toEqual({ kind: 'range', range: '90m' })
    expect(selectionFor({ from: nowMs - 2 * DAY, to: nowMs }, nowMs)).toEqual({ kind: 'range', range: '2d' })
    const fixed = selectionFor({ from: nowMs - 6 * HOUR, to: nowMs - 3 * HOUR }, nowMs)
    expect(fixed.kind).toBe('custom')
  })
  it('round-trips a selection through the URL', () => {
    const sel = selectionFor({ from: nowMs - 6 * HOUR, to: nowMs - 3 * HOUR }, nowMs)
    const p = new URLSearchParams()
    for (const [k, v] of Object.entries(params(sel))) if (v) p.set(k, v)
    expect(selection(p)).toEqual(sel)
  })
})

describe('run time against its baseline', () => {
  it('writes the change of the median with its sign, in the reader language', () => {
    expect(changeText(1.34)).toBe('+34%')
    expect(changeText(0.8)).toBe('−20%')
    expect(changeText(1)).toBe('±0%')
    setLangForTest('tr')
    expect(changeText(1.34)).toBe('+%34')
    setLangForTest('en')
  })

  it('writes sub-second run times in milliseconds, not as 0 s and 1 s', () => {
    expect(runTime(400, 0)).toBe('400 ms')
    expect(runTime(900, 1)).toBe('900 ms')
    expect(runTime(1450, 1)).toBe('1.5 s')
    expect(runTime(125_000, 125)).toBe('2m 05s')
    expect(runTime(undefined, 3)).toBe('3 s')
    expect(runTime(null, null)).toBe('—')
  })
})

describe('image inventory notes', () => {
  it('words the API note codes in the reader language', () => {
    expect(noteWords({ code: 'until_unknown' })).toBe('It stopped being current; when is not known.')
    setLangForTest('tr')
    expect(noteWords({ code: 'image_truncated', params: { bytes: '512' } })).toBe("Referans, OpsTide'ın sakladığı yerde 512 baytta kesilmiş olabilir; saklandığı gibi gösteriliyor.")
    setLangForTest('en')
    expect(noteWords({ code: 'something_new' })).toBe('something_new')
  })
})
