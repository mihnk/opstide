import type { Day, Exception } from './api'
import { duration, hhmm } from './time'

const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`

/** The sentence at the top of Today, from the day's counts. */
export function daySentence(d: Day, today: boolean): string {
  const c = d.counts
  const tz = d.timeZone
  const parts: string[] = []
  const lead = today ? `By ${hhmm(d.now, tz)} OpsTide saw` : 'OpsTide saw'
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
  if (c.missed) parts.push(`${plural(c.missed, 'expected run')} did not start.`)
  if (c.skipped) parts.push(`${plural(c.skipped, 'run')} skipped because the previous one was still running.`)
  if (c.unconfirmed) {
    const gaps = d.gaps.map((g) => `${hhmm(g.from, tz)}–${hhmm(g.to, tz)}`)
    const when = gaps.length ? `: collection paused ${joinAnd(gaps)}` : ''
    parts.push(`${c.unconfirmed === 1 ? 'One expected run' : `${c.unconfirmed} expected runs`} could not be confirmed${when}.`)
  }
  if (today && c.stillDue) parts.push(`${plural(c.stillDue, 'more run')} ${c.stillDue === 1 ? 'is' : 'are'} expected before midnight.`)
  return parts.join(' ')
}

export function joinAnd(xs: string[]): string {
  if (xs.length <= 1) return xs.join('')
  return `${xs.slice(0, -1).join(', ')} and ${xs[xs.length - 1]}`
}

/** Title and explanation of an exception, as Today lists it. */
export function exceptionText(e: Exception, tz: string): { title: string; detail: string } {
  const sched = e.scheduledAt ? `The ${hhmm(e.scheduledAt, tz)} run` : 'A run'
  const vs = e.expectedSeconds && e.seconds ? ` It took ${duration(e.seconds)} against an expected ${duration(e.expectedSeconds)}.` : ''
  const who = [e.runbookUrl ? 'Runbook linked' : '', e.owner ?? ''].filter(Boolean).join(' · ')
  const tail = who ? ` ${who}.` : ''
  switch (e.kind) {
    case 'failed': {
      const tries = e.attempts && e.attempts > 1 ? ` after ${plural(e.attempts, 'attempt')}` : ''
      const reason = e.reason ? ` (${e.reason})` : ''
      return { title: `${e.name} failed`, detail: `${sched} failed${tries}${reason}.${vs}${tail}` }
    }
    case 'lost':
      return { title: `${e.name}: outcome unknown`, detail: `${sched}'s Job was removed before OpsTide saw it finish.${tail}` }
    case 'missed':
      return { title: `${e.name} did not start`, detail: `Expected at ${hhmm(e.at, tz)}. ${e.why ?? ''}${tail}` }
    case 'unconfirmed':
      return {
        title: `${e.name} not confirmed`,
        detail: `Expected at ${hhmm(e.at, tz)}, inside a collection gap. No Job was seen, but OpsTide was not watching then, so it does not call it missed.`,
      }
    case 'skipped':
      return { title: `${e.name} skipped`, detail: `Expected at ${hhmm(e.at, tz)}. ${e.why ?? ''}` }
    case 'slow':
      return { title: `${e.name} ran long`, detail: `${sched} succeeded.${vs}` }
  }
}
