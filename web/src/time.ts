// Formatting in a chosen IANA time zone. Everything the API sends is UTC.

import { locale, t as tr } from './i18n'

const cache = new Map<string, Intl.DateTimeFormat>()

function fmt(tz: string, opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = locale() + tz + JSON.stringify(opts)
  let f = cache.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat(locale(), { timeZone: tz, ...opts })
    cache.set(key, f)
  }
  return f
}

const toDate = (t: string | number | Date) => (t instanceof Date ? t : new Date(t))

/** 08:15 */
export function hhmm(t: string | number | Date, tz: string): string {
  return fmt(tz, { hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }).format(toDate(t))
}

/** 08:15:40 */
export function hhmmss(t: string | number | Date, tz: string): string {
  return fmt(tz, { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' }).format(toDate(t))
}

/** Tuesday 4 November */
export function longDay(t: string | number | Date, tz: string): string {
  return fmt(tz, { weekday: 'long', day: 'numeric', month: 'long' }).format(toDate(t))
}

/** Tue 4 */
export function shortDay(t: string | number | Date, tz: string): string {
  return fmt(tz, { weekday: 'short', day: 'numeric' }).format(toDate(t))
}

/** YYYY-MM-DD of t in tz. */
export function isoDate(t: string | number | Date, tz: string): string {
  const parts = fmt(tz, { year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(toDate(t))
  const get = (k: string) => parts.find((p) => p.type === k)?.value ?? ''
  return `${get('year')}-${get('month')}-${get('day')}`
}

/** "today 08:00", "Mon 14:30", or "4 Nov 08:00" when more than a week away. */
export function relativeTime(t: string, now: string, tz: string): string {
  const d = isoDate(t, tz)
  if (d === isoDate(now, tz)) return tr('today {time}', { time: hhmm(t, tz) })
  const diff = Math.abs(new Date(t).getTime() - new Date(now).getTime())
  if (diff < 6 * 86400e3) return `${fmt(tz, { weekday: 'short' }).format(new Date(t))} ${hhmm(t, tz)}`
  return `${fmt(tz, { day: 'numeric', month: 'short' }).format(new Date(t))} ${hhmm(t, tz)}`
}

/** 15m 37s, 2h 05m, 40 s */
export function duration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds))
  if (s < 60) return tr('{n} s', { n: s })
  const m = Math.floor(s / 60)
  if (m < 60) return tr('{m}m {s}s', { m, s: String(s % 60).padStart(2, '0') })
  const h = Math.floor(m / 60)
  if (h < 48) return tr('{h}h {m}m', { h, m: String(m % 60).padStart(2, '0') })
  return tr('{d} d {h} h', { d: Math.floor(h / 24), h: h % 24 })
}

/** "4 s ago", "3 min ago", "2 h ago". */
export function ago(t: string, nowMs: number): string {
  const s = Math.max(0, Math.round((nowMs - new Date(t).getTime()) / 1000))
  if (s < 90) return tr('{n} s ago', { n: s })
  if (s < 5400) return tr('{n} min ago', { n: Math.round(s / 60) })
  if (s < 172800) return tr('{n} h ago', { n: Math.round(s / 3600) })
  return tr('{n} d ago', { n: Math.round(s / 86400) })
}

/** UTC offset of tz at t, as "UTC+03". */
export function offset(tz: string, t: Date = new Date()): string {
  const name = fmt(tz, { timeZoneName: 'shortOffset' }).formatToParts(t).find((p) => p.type === 'timeZoneName')?.value ?? ''
  const m = /GMT([+-])(\d+)(?::(\d+))?/.exec(name)
  if (!m) return 'UTC'
  return `UTC${m[1]}${m[2].padStart(2, '0')}${m[3] ? ':' + m[3] : ''}`
}

/** The browser's zone. */
export function browserZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
}

/** The day before or after an ISO date. */
export function shiftDate(date: string, days: number): string {
  const d = new Date(`${date}T12:00:00Z`)
  d.setUTCDate(d.getUTCDate() + days)
  return d.toISOString().slice(0, 10)
}

export function offsetMs(tz: string, at: Date): number {
  const p = new Intl.DateTimeFormat('en-US', {
    timeZone: tz, hourCycle: 'h23', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
  }).formatToParts(at)
  const g = (t: string) => Number(p.find((x) => x.type === t)?.value)
  return Date.UTC(g('year'), g('month') - 1, g('day'), g('hour'), g('minute'), g('second')) - at.getTime()
}

/** "2026-11-04T08:51" read as wall-clock time in tz, as epoch ms. NaN when it is not a date. */
export function zonedToUtc(local: string, tz: string): number {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(local)) return NaN
  const guess = Date.parse(`${local}:00Z`)
  if (isNaN(guess)) return NaN
  // The offset depends on the instant, which depends on the offset: two rounds settle it, daylight saving included.
  let t = guess - offsetMs(tz, new Date(guess))
  t = guess - offsetMs(tz, new Date(t))
  return t
}

/** Epoch ms as "2026-11-04T08:51" in tz, for a datetime-local input. */
export function utcToZoned(ms: number, tz: string): string {
  return new Date(ms + offsetMs(tz, new Date(ms))).toISOString().slice(0, 16)
}
