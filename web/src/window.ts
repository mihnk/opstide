import { hhmm, isoDate, longDay, shiftDate, shortDay } from './time'

// The window the Overview shows lives in the URL, so a link shares it:
//   (nothing)                 today
//   ?date=2026-11-03          one calendar day
//   ?range=3h                 the last 3 hours, following now (also 90m, 5d)
//   ?from=…&to=…              a fixed window (RFC 3339)

export type Selection =
  | { kind: 'today' }
  | { kind: 'date'; date: string }
  | { kind: 'range'; range: string }
  | { kind: 'custom'; from: string; to: string }

export const MINUTE = 60_000
export const HOUR = 60 * MINUTE
export const DAY = 24 * HOUR
export const MIN_WINDOW = 5 * MINUTE
// The same limit as the server: seven days, plus the hour that covers a daylight-saving day.
export const MAX_WINDOW = 7 * DAY + HOUR

export const presets: { key: string; label: string; range?: string }[] = [
  { key: 'today', label: 'Today' },
  { key: 'yesterday', label: 'Yesterday' },
  { key: '1h', label: '1 h', range: '1h' },
  { key: '3h', label: '3 h', range: '3h' },
  { key: '6h', label: '6 h', range: '6h' },
  { key: '12h', label: '12 h', range: '12h' },
  { key: '24h', label: '24 h', range: '24h' },
  { key: '3d', label: '3 d', range: '3d' },
  { key: '5d', label: '5 d', range: '5d' },
]

const rangeRe = /^[1-9]\d*[mhd]$/

export function selection(p: URLSearchParams): Selection {
  const range = p.get('range')
  if (range && rangeRe.test(range)) return { kind: 'range', range }
  const from = p.get('from')
  const to = p.get('to')
  if (from && to && Date.parse(to) - Date.parse(from) >= MIN_WINDOW) return { kind: 'custom', from, to }
  const date = p.get('date')
  if (date && /^\d{4}-\d{2}-\d{2}$/.test(date)) return { kind: 'date', date }
  return { kind: 'today' }
}

/** The query string for /api/v1/range. */
export function rangeQuery(sel: Selection, tz: string): string {
  const q = new URLSearchParams()
  q.set('tz', tz)
  if (sel.kind === 'range') q.set('within', sel.range)
  else if (sel.kind === 'custom') {
    q.set('from', new Date(sel.from).toISOString())
    q.set('to', new Date(sel.to).toISOString())
  } else if (sel.kind === 'date') q.set('date', sel.date)
  return `?${q.toString()}`
}

/** URL parameter changes (for withParams) that select a window. */
export function params(sel: Selection): Record<string, string | null> {
  const none = { range: null, date: null, from: null, to: null }
  switch (sel.kind) {
    case 'today':
      return none
    case 'date':
      return { ...none, date: sel.date }
    case 'range':
      return { ...none, range: sel.range }
    case 'custom':
      return { ...none, from: sel.from, to: sel.to }
  }
}

export function presetSelection(key: string, nowIso: string, tz: string): Selection {
  if (key === 'today') return { kind: 'today' }
  if (key === 'yesterday') return { kind: 'date', date: shiftDate(isoDate(nowIso, tz), -1) }
  return { kind: 'range', range: presets.find((p) => p.key === key)?.range ?? '24h' }
}

/** Which preset is pressed, if any. */
export function pressedPreset(sel: Selection, nowIso: string, tz: string): string | null {
  switch (sel.kind) {
    case 'today':
      return 'today'
    case 'date':
      return sel.date === shiftDate(isoDate(nowIso, tz), -1) ? 'yesterday' : null
    case 'range':
      return presets.find((p) => p.range === sel.range)?.key ?? null
    default:
      return null
  }
}

/** "Last 3 hours", "Last 90 minutes", "Last 5 days". */
export function rangeWords(range: string): string {
  const n = parseInt(range, 10)
  const unit = range.slice(-1)
  const [one, many] = unit === 'd' ? ['day', 'days'] : unit === 'h' ? ['hour', 'hours'] : ['minute', 'minutes']
  if (unit === 'm' && n % 60 === 0 && n >= 60) return rangeWords(`${n / 60}h`)
  return n === 1 ? `Last ${one}` : `Last ${n} ${many}`
}

/** "08:51 – 11:51" or, over midnight, "Wed 4 00:00 – Thu 5 11:51". */
export function spanWords(from: string, to: string, tz: string): string {
  if (isoDate(from, tz) === isoDate(to, tz)) return `${hhmm(from, tz)} – ${hhmm(to, tz)}`
  return `${shortDay(from, tz)} ${hhmm(from, tz)} – ${shortDay(to, tz)} ${hhmm(to, tz)}`
}

/** "36 h", "45 min", "3 d". */
export function lengthWords(ms: number): string {
  if (ms >= 2 * DAY) return `${Math.round(ms / DAY)} d`
  if (ms >= 2 * HOUR) return `${Math.round(ms / HOUR)} h`
  return `${Math.round(ms / MINUTE)} min`
}

export function title(sel: Selection, from: string, to: string, tz: string, nowIso: string): { title: string; sub: string } {
  const live = Date.parse(to) >= Date.parse(nowIso) - 2 * MINUTE
  switch (sel.kind) {
    case 'today':
      return { title: 'Today', sub: `${longDay(from, tz)} · ${hhmm(from, tz)} – 24:00` }
    case 'date': {
      const yesterday = sel.date === shiftDate(isoDate(nowIso, tz), -1)
      return { title: yesterday ? 'Yesterday' : longDay(from, tz), sub: yesterday ? longDay(from, tz) : sel.date }
    }
    case 'range':
      return { title: rangeWords(sel.range), sub: `${spanWords(from, to, tz)}${live ? ' · live' : ''}` }
    case 'custom':
      return { title: spanWords(from, to, tz), sub: `${lengthWords(Date.parse(to) - Date.parse(from))}${live ? ' · live' : ''}` }
  }
}

export interface Win { from: number; to: number }

const clamp = (x: number, lo: number, hi: number) => Math.min(Math.max(x, lo), hi)

/** Slides the window by dx, keeping its length, inside [lo, hi]. */
export function moveWindow(w: Win, dx: number, lo: number, hi: number): Win {
  const len = w.to - w.from
  const from = clamp(w.from + dx, lo, Math.max(lo, hi - len))
  return { from, to: from + len }
}

/** Moves one edge by dx; the window stays between 5 minutes and 7 days and inside [lo, hi]. */
export function resizeWindow(w: Win, edge: 'from' | 'to', dx: number, lo: number, hi: number): Win {
  if (edge === 'from') {
    const from = clamp(w.from + dx, Math.max(lo, w.to - MAX_WINDOW), w.to - MIN_WINDOW)
    return { from, to: w.to }
  }
  const to = clamp(w.to + dx, w.from + MIN_WINDOW, Math.min(hi, w.from + MAX_WINDOW))
  return { from: w.from, to }
}

/** A window of the same length centred on at, inside [lo, hi]. */
export function centreWindow(w: Win, at: number, lo: number, hi: number): Win {
  const len = w.to - w.from
  return moveWindow({ from: at - len / 2, to: at + len / 2 }, 0, lo, hi)
}

/**
 * The selection a window means. One that ends at now follows it (?range=…),
 * so the link stays live; any other is fixed. Times are cut to the minute.
 */
export function selectionFor(w: Win, nowMs: number): Selection {
  const from = Math.floor(w.from / MINUTE) * MINUTE
  const to = Math.floor(w.to / MINUTE) * MINUTE
  if (w.to >= nowMs - 2 * MINUTE && w.to <= nowMs + 2 * MINUTE) {
    const minutes = Math.max(5, Math.round((nowMs - from) / MINUTE))
    const d = minutes % (24 * 60) === 0 ? `${minutes / (24 * 60)}d` : minutes % 60 === 0 ? `${minutes / 60}h` : `${minutes}m`
    return { kind: 'range', range: d }
  }
  return { kind: 'custom', from: new Date(from).toISOString(), to: new Date(to).toISOString() }
}
