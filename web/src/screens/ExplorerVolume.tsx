import { useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from 'react'
import type { ExplorerBucket, ExplorerContext, ExplorerHistogram } from '../explorer'
import { t, tn } from '../i18n'
import { hhmm, isoDate, offsetMs, weekday } from '../time'

// The volume chart over the results: how many records the filters match per stretch of the range, stacked by class.
// Stretches OpsTide was not watching are hatched across the whole height, so an empty stretch there is read as unknown,
// not as quiet. Clicking a bar, or dragging across several, narrows the range to them; with the keyboard, the arrow
// keys pick a stretch (Shift widens it) and Enter narrows to it.

const classes = ['error', 'warning', 'change', 'other'] as const
type Cls = (typeof classes)[number]
const total = (b: ExplorerBucket) => b.error + b.warning + b.change + b.other

export const classWord: Record<Cls, () => string> = {
  error: () => t('error'),
  warning: () => t('warning'),
  change: () => t('change'),
  other: () => t('other'),
}

/** The stretches not watched in the context: watch gaps, and what is older than kept or before the first observation. */
export function notWatched(ctx: ExplorerContext | undefined): { from: number; to: number; why: string }[] {
  if (!ctx) return []
  const out: { from: number; to: number; why: string }[] = []
  for (const s of ctx.scopes) {
    for (const g of s.gaps) out.push({ from: Date.parse(g.from), to: Date.parse(g.to), why: `${t(s.label)}${s.namespace ? ` (${s.namespace})` : ''}` })
  }
  for (const b of ctx.bands) {
    if (b.kind !== 'observable') out.push({ from: Date.parse(b.from), to: Date.parse(b.to), why: t(b.kind.replace(/_/g, ' ')) })
  }
  return out
}

export function Volume({ hist, ctx, tz, live, onNarrow, head }: {
  hist: ExplorerHistogram
  ctx?: ExplorerContext
  tz: string
  /** The range ends now: the last label says so. */
  live: boolean
  onNarrow: (from: string, to: string) => void
  head: ReactNode
}) {
  const bs = hist.buckets
  const step = hist.bucketSeconds * 1000
  const a = bs.length ? Date.parse(bs[0].start) : Date.parse(hist.from)
  const b = bs.length ? Date.parse(bs[bs.length - 1].start) + step : Date.parse(hist.to)
  const pct = (ms: number) => ((Math.min(Math.max(ms, a), b) - a) / (b - a)) * 100
  const max = Math.max(1, ...bs.map(total))
  const gaps = notWatched(ctx).filter((g) => g.to > a && g.from < b)

  const [hover, setHover] = useState<number | null>(null)
  const [cursor, setCursor] = useState<number | null>(null)
  const [anchor, setAnchor] = useState<number | null>(null)
  const [sel, setSel] = useState<[number, number] | null>(null)
  const ref = useRef<HTMLDivElement>(null)

  const indexAt = (x: number) => {
    const r = ref.current!.getBoundingClientRect()
    return Math.min(bs.length - 1, Math.max(0, Math.floor(((x - r.left) / r.width) * bs.length)))
  }
  const narrow = (i: number, j: number) => {
    const [p, q] = [Math.min(i, j), Math.max(i, j)]
    const from = Math.max(Date.parse(bs[p].start), Date.parse(hist.from))
    const to = Math.min(Date.parse(bs[q].start) + step, Date.parse(hist.to))
    setSel(null)
    setAnchor(null)
    onNarrow(new Date(from).toISOString().replace('.000Z', 'Z'), new Date(to).toISOString().replace('.000Z', 'Z'))
  }

  const down = (e: PointerEvent<HTMLDivElement>) => {
    if (!bs.length || e.button !== 0) return
    const i = indexAt(e.clientX)
    ref.current?.setPointerCapture?.(e.pointerId)
    setAnchor(i)
    setSel([i, i])
  }
  const move = (e: PointerEvent<HTMLDivElement>) => {
    if (!bs.length) return
    const i = indexAt(e.clientX)
    setHover(i)
    if (anchor !== null) setSel([Math.min(anchor, i), Math.max(anchor, i)])
  }
  const up = () => {
    if (anchor !== null && sel) narrow(sel[0], sel[1])
  }

  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    if (!bs.length) return
    const cur = cursor ?? bs.length - 1
    let next: number
    if (e.key === 'ArrowRight') next = Math.min(bs.length - 1, cur + 1)
    else if (e.key === 'ArrowLeft') next = Math.max(0, cur - 1)
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = bs.length - 1
    else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      if (sel) narrow(sel[0], sel[1])
      else narrow(cur, cur)
      return
    } else if (e.key === 'Escape' && sel) {
      e.stopPropagation()
      setSel(null)
      setAnchor(null)
      return
    } else return
    e.preventDefault()
    setCursor(next)
    if (e.shiftKey) {
      const from = anchor ?? cur
      setAnchor(from)
      setSel([Math.min(from, next), Math.max(from, next)])
    } else {
      setAnchor(null)
      setSel(null)
    }
  }

  const span = (i: number) => `${hhmm(bs[i].start, tz)}–${hhmm(Date.parse(bs[i].start) + step, tz)}`
  const describe = (i: number) => {
    const x = bs[i]
    const parts = classes.filter((c) => x[c] > 0).map((c) => `${x[c]} ${classWord[c]()}`)
    const s = Date.parse(x.start)
    const gap = gaps.filter((g) => g.from < s + step && g.to > s)
    return {
      when: `${weekday(x.start, tz)} ${span(i)}`,
      what: tn(total(x), '{n} record', '{n} records') + (parts.length ? ` · ${parts.join(', ')}` : ''),
      gap: gap.length ? t('not watched for part of it: {list}', { list: [...new Set(gap.map((g) => g.why))].join(', ') }) : '',
    }
  }
  const shown = hover ?? cursor
  const tip = shown !== null && bs[shown] ? describe(shown) : null

  // Labels on whole stretches of the reader's clock (every 6 hours of a day, every day of a week), the day where it
  // changes, and "now" at the end of a range that follows it.
  const ticks: { at: number; label: string; edge?: 'start' | 'end' }[] = []
  const long = hist.bucketSeconds >= 86400
  const every = niceEvery(bs.length, hist.bucketSeconds)
  let lastDay = ''
  bs.forEach((x, i) => {
    const s = Date.parse(x.start)
    if (s < a || Math.round((s + offsetMs(tz, new Date(s))) / step) % every !== 0) return
    const at = (i / bs.length) * 100
    if (at > 86) return
    const day = isoDate(s, tz)
    ticks.push({ at, label: long ? weekday(s, tz) : `${day !== lastDay ? `${weekday(s, tz)} ` : ''}${hhmm(s, tz)}`, edge: at < 4 ? 'start' : undefined })
    lastDay = day
  })
  ticks.push({ at: 100, label: live ? t('now') : hhmm(b, tz), edge: 'end' })

  return (
    <section className="ex-vol" aria-label={t('Record volume')}>
      <div className="vol-head">
        {head}
        <span className="grow" />
        <span className="vol-legend" aria-hidden="true">
          {classes.map((c) => <span key={c}><i className={`sw-${c}`} />{classWord[c]()}</span>)}
          <span><i className="sw-gap" />{t('not watched')}</span>
        </span>
      </div>
      <div ref={ref} className={`vol${anchor !== null ? ' dragging' : ''}`} tabIndex={0} role="group"
        aria-label={t('Records per {step} over the range, by class. Arrow keys pick a stretch, Shift widens it, Enter narrows the range to it.', { step: stepWord(hist.bucketSeconds) })}
        onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerLeave={() => setHover(null)} onKeyDown={key}
        onBlur={() => { setCursor(null); setSel(null); setAnchor(null) }}>
        {hist.total > 0 && <span className="vol-max mono" aria-hidden="true">{max}</span>}
        {gaps.map((g, i) => (
          <i key={i} className="vol-gap" style={{ left: `${pct(g.from)}%`, width: `max(3px, ${pct(g.to) - pct(g.from)}%)` }} />
        ))}
        <div className="vol-bars" style={{ gridTemplateColumns: `repeat(${Math.max(1, bs.length)}, minmax(0, 1fr))` }} aria-hidden="true">
          {bs.map((x, i) => {
            const n = total(x)
            const inSel = sel && i >= sel[0] && i <= sel[1]
            return (
              <span key={x.start} className={`vol-col${inSel ? ' sel' : ''}${shown === i ? ' on' : ''}`}>
                {n > 0 && (
                  <span className="vol-stack" style={{ height: `max(3px, ${(n / max) * 100}%)` }}>
                    {classes.map((c) => x[c] > 0 && <span key={c} className={`seg-${c}`} style={{ flexGrow: x[c] }} />)}
                  </span>
                )}
              </span>
            )
          })}
        </div>
        {tip && shown !== null && (
          <span className={`vol-tip${shown > bs.length * 0.7 ? ' left' : ''}`} style={{ left: `${((shown + 0.5) / bs.length) * 100}%` }} aria-hidden="true">
            <b className="mono">{tip.when}</b><span>{tip.what}</span>{tip.gap && <span className="vol-tip-gap">{tip.gap}</span>}
          </span>
        )}
      </div>
      <span className="sr" role="status">{cursor !== null && bs[cursor] ? (({ when, what, gap }) => `${when}: ${what}${gap ? `; ${gap}` : ''}`)(describe(cursor)) : ''}</span>
      <div className="vol-axis mono" aria-hidden="true">
        {ticks.map((x, i) => <span key={i} className={x.edge} style={{ left: `${x.at}%` }}>{x.label}</span>)}
      </div>
    </section>
  )
}

function stepWord(seconds: number): string {
  if (seconds % 86400 === 0) return seconds === 86400 ? t('day') : t('{n} days', { n: seconds / 86400 })
  if (seconds % 3600 === 0) return seconds === 3600 ? t('hour') : t('{n} hours', { n: seconds / 3600 })
  return t('{n} minutes', { n: Math.round(seconds / 60) })
}

/** How many buckets between two axis labels: a whole number of clock units that gives four to six labels. */
function niceEvery(n: number, seconds: number): number {
  const units = seconds < 3600 ? [1, 2, 3, 5, 6, 10, 15, 30, 60].map((m) => (m * 60) / seconds) : [1, 2, 3, 4, 6, 12, 24, 48].map((h) => (h * 3600) / seconds)
  const ok = units.filter((u) => Number.isInteger(u) && u >= 1)
  return ok.find((u) => n / u <= 6) ?? ok[ok.length - 1] ?? Math.max(1, Math.round(n / 5))
}
