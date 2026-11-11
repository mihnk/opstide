import type { Exception, Range } from './api'
import { t, tn } from './i18n'
import { duration, hhmm, isoDate, shortDay } from './time'

const plural = (n: number, one: string, many = one + 's') => tn(n, `{n} ${one}`, `{n} ${many}`)

export function joinAnd(xs: string[]): string {
  if (xs.length <= 1) return xs.join('')
  return `${xs.slice(0, -1).join(', ')} ${t('and')} ${xs[xs.length - 1]}`
}

const sameDate = (a: string, b: string, tz: string) => isoDate(a, tz) === isoDate(b, tz)

/** "10:12–10:30", with the day when it is not the window's first. */
function stretch(from: string, to: string, tz: string, windowFrom: string): string {
  const sameDay = sameDate(from, windowFrom, tz)
  const day = (x: string) => (sameDay ? '' : `${shortDay(x, tz)} `)
  const sameEnd = sameDate(from, to, tz)
  return `${day(from)}${hhmm(from, tz)}–${sameEnd ? '' : `${shortDay(to, tz)} `}${hhmm(to, tz)}`
}

/** A length of time in words: "40 s", "12 min", "2 h", "2 h 05 min", "3 d 4 h". */
function span(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  if (s < 60) return t('{n} s', { n: s })
  const m = Math.floor(s / 60)
  if (m < 60) return t('{n} min', { n: m })
  const h = Math.floor(m / 60)
  if (h < 48) return m % 60 === 0 ? t('{n} h', { n: h }) : t('{h} h {m} min', { h, m: String(m % 60).padStart(2, '0') })
  return t('{d} d {h} h', { d: Math.floor(h / 24), h: h % 24 })
}

/** The sentence at the top of the Overview, from the window's counts. */
export function rangeSentence(d: Range, tz: string, today: boolean): string {
  const c = d.counts
  const parts: string[] = []
  const time = hhmm(d.now, tz)
  if (c.observed === 0) {
    parts.push(today ? t('By {time} OpsTide saw no runs.', { time }) : t('In this window OpsTide saw no runs.'))
  } else {
    const how: string[] = []
    if (c.succeeded) how.push(t('{n} succeeded', { n: c.succeeded }))
    if (c.failed) how.push(t('{n} failed', { n: c.failed }))
    if (c.running) how.push(tn(c.running, '{n} is still running', '{n} are still running'))
    if (c.unknown) how.push(t('{n} ended unseen', { n: c.unknown }))
    const vars = { time, runs: plural(c.observed, 'run'), how: joinAnd(how) }
    parts.push(today ? t('By {time} OpsTide saw {runs}: {how}.', vars) : t('In this window OpsTide saw {runs}: {how}.', vars))
  }
  if (d.watchedSince && Date.parse(d.watchedSince) > Date.parse(d.from)) {
    parts.push(t('OpsTide started watching at {time}; before that it says nothing.', { time: stretch(d.watchedSince, d.watchedSince, tz, d.from).split('–')[0] }))
  }
  if (c.missed) parts.push(tn(c.missed, '{n} expected run did not start.', '{n} expected runs did not start.'))
  if (c.skipped) parts.push(tn(c.skipped, '{n} run skipped because the previous one was still running.', '{n} runs skipped because the previous one was still running.'))
  if (d.gaps.length) {
    const total = d.gaps.reduce((n, g) => n + Date.parse(g.to) - Date.parse(g.from), 0)
    const one = d.gaps.length === 1
    const vars = { when: one ? stretch(d.gaps[0].from, d.gaps[0].to, tz, d.from) : '', count: d.gaps.length, span: span(total), n: c.unconfirmed }
    if (!c.unconfirmed) parts.push(one ? t('It was not watching {when}.', vars) : t('It was not watching {count} stretches, {span} in all.', vars))
    else if (one) parts.push(tn(c.unconfirmed, 'It was not watching {when}, so {n} expected run in that time is not confirmed.', 'It was not watching {when}, so {n} expected runs in that time are not confirmed.', vars))
    else parts.push(tn(c.unconfirmed, 'It was not watching {count} stretches, {span} in all, so {n} expected run in that time is not confirmed.', 'It was not watching {count} stretches, {span} in all, so {n} expected runs in that time are not confirmed.', vars))
  }
  if (c.stillDue) {
    parts.push(today
      ? tn(c.stillDue, '{n} more run is expected before midnight.', '{n} more runs are expected before midnight.')
      : tn(c.stillDue, '{n} more run is expected.', '{n} more runs are expected.'))
  }
  return parts.join(' ')
}

/** Title, detail and the clock text of one grouped exception. */
export function exceptionText(e: Exception, tz: string, windowFrom: string, windowTo: string = windowFrom): { when: string; title: string; detail: string } {
  const multiDay = !sameDate(windowFrom, windowTo, tz)
  const clock = (x: string) => (multiDay ? `${shortDay(x, tz)} ${hhmm(x, tz)}` : hhmm(x, tz))
  const name = e.name ?? t('An operation')
  const times = (once: string, many: string) => (e.count > 1 ? t(many, { name, n: e.count }) : t(once, { name }))
  const took = { d: duration(e.seconds ?? 0), e: duration(e.expectedSeconds ?? 0) }
  const vs = e.expectedSeconds && e.seconds ? ' ' + (e.count > 1 ? t('The latest took {d} against an expected {e}.', took) : t('It took {d} against an expected {e}.', took)) : ''
  const who = [e.runbookUrl ? t('Runbook linked') : '', e.owner ?? ''].filter(Boolean).join(' · ')
  const tail = who ? ` ${who}.` : ''
  const tries = e.attempts && e.attempts > 1 ? tn(e.attempts, ' after {n} attempt', ' after {n} attempts') : ''
  const reason = e.reason ? ` (${e.reason})` : ''
  const latest = t('Latest {time}.', { time: stretch(e.at, e.at, tz, windowFrom).split('–')[0] })
  switch (e.kind) {
    case 'failing': {
      const from = e.from ?? e.at
      const to = e.to ?? e.at
      const mins = Math.max(1, Math.round((Date.parse(to) - Date.parse(from)) / 60000))
      return {
        when: stretch(from, to, tz, windowFrom),
        title: t('{name}: every run failed for {span}', { name, span: span(mins * 60000) }),
        detail: t('{runs} in a row ended{tries}{reason}.', { runs: plural(e.count, 'run'), tries, reason }) + tail,
      }
    }
    case 'failed':
      return {
        when: clock(e.at),
        title: times('{name} failed', '{name} failed {n} times'),
        detail: e.count > 1
          ? t('Runs ended{tries}{reason}, not in a row.', { tries, reason }) + ` ${latest}${vs}${tail}`
          : (e.scheduledAt ? t('The {time} run failed{tries}{reason}.', { time: hhmm(e.scheduledAt, tz), tries, reason }) : t('The run failed{tries}{reason}.', { tries, reason })) + vs + tail,
      }
    case 'lost':
      return {
        when: clock(e.at),
        title: times('{name}: outcome unknown', '{name}: outcome unknown {n} times'),
        detail: (e.count > 1 ? t('Jobs were removed before OpsTide saw them finish.') : t('The Job was removed before OpsTide saw it finish.')) + tail,
      }
    case 'slow':
      return {
        when: clock(e.at),
        title: times('{name} ran long', '{name} ran long {n} times'),
        detail: (e.count > 1 ? `${t('The longest took {d} against an expected {e}.', took)} ${latest}` : t('It took {d} against an expected {e}.', took)).trim(),
      }
    case 'missed':
      return { when: clock(e.at), title: times('{name} did not start', '{name} did not start {n} times'), detail: `${e.why ?? ''}${e.count > 1 ? ` ${latest}` : ''}${tail}`.trim() }
    case 'skipped':
      return { when: clock(e.at), title: times('{name} skipped', '{name} skipped {n} times'), detail: e.why ?? '' }
    case 'gap': {
      const from = e.from ?? e.at
      const to = e.to ?? e.at
      const ms = Date.parse(to) - Date.parse(from)
      const inside = e.expectedRuns
        ? t('{ops}, {runs} fell inside it. They are not confirmed, and OpsTide does not call them missed.', { ops: plural(e.operations ?? 0, 'operation'), runs: plural(e.expectedRuns, 'expected run') })
        : t('Nothing was expected in it.')
      return { when: stretch(from, to, tz, windowFrom), title: t('Not watching for {span}', { span: span(ms) }), detail: inside }
    }
  }
}
