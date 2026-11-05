import type { Exception, Range } from './api'
import { duration, hhmm, shortDay } from './time'

const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`

export function joinAnd(xs: string[]): string {
  if (xs.length <= 1) return xs.join('')
  return `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`
}

/** "10:12–10:30", with the day when it is not the window's first. */
function stretch(from: string, to: string, tz: string, windowFrom: string): string {
  const sameDay = new Date(from).toLocaleDateString('en-GB', { timeZone: tz }) === new Date(windowFrom).toLocaleDateString('en-GB', { timeZone: tz })
  const day = (t: string) => (sameDay ? '' : `${shortDay(t, tz)} `)
  const sameEnd = new Date(from).toLocaleDateString('en-GB', { timeZone: tz }) === new Date(to).toLocaleDateString('en-GB', { timeZone: tz })
  return `${day(from)}${hhmm(from, tz)}–${sameEnd ? '' : `${shortDay(to, tz)} `}${hhmm(to, tz)}`
}

function span(ms: number): string {
  return duration(ms / 1000).replace(/^(\d+)m \d\ds$/, '$1 min').replace(/^(\d+)h (\d\d)m$/, (_, h, m) => `${h} h${m === '00' ? '' : ` ${m} min`}`)
}

/** The sentence at the top of the Overview, from the window's counts. */
export function rangeSentence(d: Range, tz: string, today: boolean): string {
  const c = d.counts
  const parts: string[] = []
  const lead = today ? `By ${hhmm(d.now, tz)} OpsTide saw` : 'In this window OpsTide saw'
  if (c.observed === 0) {
    parts.push(`${lead} no runs.`)
  } else {
    const how: string[] = []
    if (c.succeeded) how.push(`${c.succeeded} succeeded`)
    if (c.failed) how.push(`${c.failed} failed`)
    if (c.running) how.push(`${c.running} ${c.running === 1 ? 'is' : 'are'} still running`)
    if (c.unknown) how.push(`${c.unknown} ended unseen`)
    parts.push(`${lead} ${plural(c.observed, 'run')}: ${joinAnd(how)}.`)
  }
  if (d.watchedSince && Date.parse(d.watchedSince) > Date.parse(d.from)) {
    parts.push(`OpsTide started watching at ${stretch(d.watchedSince, d.watchedSince, tz, d.from).split('–')[0]}; before that it says nothing.`)
  }
  if (c.missed) parts.push(`${plural(c.missed, 'expected run')} did not start.`)
  if (c.skipped) parts.push(`${plural(c.skipped, 'run')} skipped because the previous one was still running.`)
  if (d.gaps.length) {
    const total = d.gaps.reduce((n, g) => n + Date.parse(g.to) - Date.parse(g.from), 0)
    const when = d.gaps.length === 1 ? stretch(d.gaps[0].from, d.gaps[0].to, tz, d.from) : `${d.gaps.length} stretches, ${span(total)} in all`
    const tail = c.unconfirmed ? `, so ${plural(c.unconfirmed, 'expected run')} in that time ${c.unconfirmed === 1 ? 'is' : 'are'} not confirmed` : ''
    parts.push(`It was not watching ${when}${tail}.`)
  }
  if (c.stillDue) parts.push(`${plural(c.stillDue, 'more run')} ${c.stillDue === 1 ? 'is' : 'are'} expected${today ? ' before midnight' : ''}.`)
  return parts.join(' ')
}

/** Title, detail and the clock text of one grouped exception. */
export function exceptionText(e: Exception, tz: string, windowFrom: string, windowTo: string = windowFrom): { when: string; title: string; detail: string } {
  const multiDay = new Date(windowFrom).toLocaleDateString('en-GB', { timeZone: tz }) !== new Date(windowTo).toLocaleDateString('en-GB', { timeZone: tz })
  const clock = (t: string) => (multiDay ? `${shortDay(t, tz)} ${hhmm(t, tz)}` : hhmm(t, tz))
  const name = e.name ?? 'An operation'
  const vs = e.expectedSeconds && e.seconds ? ` ${e.count > 1 ? 'The latest took' : 'It took'} ${duration(e.seconds)} against an expected ${duration(e.expectedSeconds)}.` : ''
  const who = [e.runbookUrl ? 'Runbook linked' : '', e.owner ?? ''].filter(Boolean).join(' · ')
  const tail = who ? ` ${who}.` : ''
  const tries = e.attempts && e.attempts > 1 ? ` after ${plural(e.attempts, 'attempt')}` : ''
  const reason = e.reason ? ` (${e.reason})` : ''
  const latest = `Latest ${stretch(e.at, e.at, tz, windowFrom).split('–')[0]}.`
  switch (e.kind) {
    case 'failing': {
      const from = e.from ?? e.at
      const to = e.to ?? e.at
      const mins = Math.max(1, Math.round((Date.parse(to) - Date.parse(from)) / 60000))
      return {
        when: stretch(from, to, tz, windowFrom),
        title: `${name}: every run failed for ${span(mins * 60000)}`,
        detail: `${plural(e.count, 'run')} in a row ended${tries}${reason}.${tail}`,
      }
    }
    case 'failed':
      return {
        when: clock(e.at),
        title: `${name} failed${e.count > 1 ? ` ${e.count} times` : ''}`,
        detail: e.count > 1 ? `Runs ended${tries}${reason}, not in a row. ${latest}${vs}${tail}` : `The ${e.scheduledAt ? hhmm(e.scheduledAt, tz) + ' ' : ''}run failed${tries}${reason}.${vs}${tail}`,
      }
    case 'lost':
      return { when: clock(e.at), title: `${name}: outcome unknown${e.count > 1 ? ` ${e.count} times` : ''}`, detail: `${e.count > 1 ? 'Jobs were' : "The Job was"} removed before OpsTide saw ${e.count > 1 ? 'them' : 'it'} finish.${tail}` }
    case 'slow':
      return {
        when: clock(e.at),
        title: `${name} ran long${e.count > 1 ? ` ${e.count} times` : ''}`,
        detail: `${e.count > 1 ? `The longest took ${duration(e.seconds ?? 0)}` : `It took ${duration(e.seconds ?? 0)}`} against an expected ${duration(e.expectedSeconds ?? 0)}. ${e.count > 1 ? latest : ''}`.trim(),
      }
    case 'missed':
      return { when: clock(e.at), title: `${name} did not start${e.count > 1 ? ` ${e.count} times` : ''}`, detail: `${e.why ?? ''}${e.count > 1 ? ` ${latest}` : ''}${tail}`.trim() }
    case 'skipped':
      return { when: clock(e.at), title: `${name} skipped${e.count > 1 ? ` ${e.count} times` : ''}`, detail: e.why ?? '' }
    case 'gap': {
      const from = e.from ?? e.at
      const to = e.to ?? e.at
      const ms = Date.parse(to) - Date.parse(from)
      const inside = e.expectedRuns
        ? `${plural(e.operations ?? 0, 'operation')}, ${plural(e.expectedRuns, 'expected run')} fell inside it. They are not confirmed, and OpsTide does not call them missed.`
        : 'Nothing was expected in it.'
      return { when: stretch(from, to, tz, windowFrom), title: `Not watching for ${span(ms)}`, detail: inside }
    }
  }
}
