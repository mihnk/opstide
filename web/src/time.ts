// Formatting in a chosen IANA time zone. Everything the API sends is UTC.

const cache = new Map<string, Intl.DateTimeFormat>()

function fmt(tz: string, opts: Intl.DateTimeFormatOptions): Intl.DateTimeFormat {
  const key = tz + JSON.stringify(opts)
  let f = cache.get(key)
  if (!f) {
    f = new Intl.DateTimeFormat('en-GB', { timeZone: tz, ...opts })
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
  if (d === isoDate(now, tz)) return `today ${hhmm(t, tz)}`
  const diff = Math.abs(new Date(t).getTime() - new Date(now).getTime())
  if (diff < 6 * 86400e3) return `${fmt(tz, { weekday: 'short' }).format(new Date(t))} ${hhmm(t, tz)}`
  return `${fmt(tz, { day: 'numeric', month: 'short' }).format(new Date(t))} ${hhmm(t, tz)}`
}

/** 15m 37s, 2h 05m, 40 s */
export function duration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds))
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, '0')}s`
  const h = Math.floor(m / 60)
  if (h < 48) return `${h}h ${String(m % 60).padStart(2, '0')}m`
  return `${Math.floor(h / 24)} d ${h % 24} h`
}

/** "4 s ago", "3 min ago", "2 h ago". */
export function ago(t: string, nowMs: number): string {
  const s = Math.max(0, Math.round((nowMs - new Date(t).getTime()) / 1000))
  if (s < 90) return `${s} s ago`
  if (s < 5400) return `${Math.round(s / 60)} min ago`
  if (s < 172800) return `${Math.round(s / 3600)} h ago`
  return `${Math.round(s / 86400)} d ago`
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
