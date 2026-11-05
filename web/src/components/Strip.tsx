import { useRef, useState, type KeyboardEvent, type PointerEvent } from 'react'
import { query, type Overview } from '../api'
import { useApi } from '../live'
import { hhmm, shortDay } from '../time'
import { centreWindow, DAY, HOUR, MINUTE, moveWindow, resizeWindow, selectionFor, type Selection, type Win } from '../window'

const spans = [
  ['24h', '24 h'],
  ['5d', '5 days'],
  ['7d', '7 days'],
  ['30d', '30 days'],
] as const

type Drag = { mode: 'move' | 'from' | 'to' | 'click'; startX: number; start: Win; win: Win; moved: boolean }

interface Props {
  tz: string
  span: string
  setSpan: (s: string) => void
  /** The window being shown, in epoch ms. */
  win: Win
  nowMs: number
  onChange: (sel: Selection) => void
}

/**
 * The strip above the Overview: runs per hour over the last days, failures in
 * red, the times OpsTide was not watching, and the window you are looking at.
 * Drag the window or its edges, or click the strip to move the window there.
 */
export function Strip({ tz, span, setSpan, win, nowMs, onChange }: Props) {
  const { data } = useApi<Overview>(`/api/v1/overview${query({ span, tz })}`, { keep: true })
  const [drag, setDragState] = useState<Drag | null>(null)
  // The handlers read the drag from a ref: a quick tap fires pointerup before React has re-rendered.
  const dragRef = useRef<Drag | null>(null)
  const setDrag = (d: Drag | null) => {
    dragRef.current = d
    setDragState(d)
  }
  if (!data) return <div className="empty" aria-busy="true">Loading the overview…</div>

  const lo = Date.parse(data.from)
  const hi = Date.parse(data.to)
  // A window can reach outside the strip (Yesterday on the 24 h strip). Moving it must not
  // snap it into the strip, so it may stay where it is, but not go further out.
  const loI = Math.min(lo, win.from)
  const hiI = Math.max(hi, win.to)
  const outside = win.to <= lo || win.from >= hi
  const pct = (ms: number) => `${(Math.min(Math.max((ms - lo) / (hi - lo), 0), 1) * 100).toFixed(3)}%`
  const widthPct = (a: number, b: number) => `${((Math.min(Math.max(b, lo), hi) - Math.min(Math.max(a, lo), hi)) / (hi - lo) * 100).toFixed(3)}%`
  const shown = drag?.win ?? win
  const max = Math.max(1, ...data.buckets.map((b) => b.runs))
  const bucketMs = data.bucketSeconds * 1000
  const watched = data.watchedSince ? Date.parse(data.watchedSince) : hi

  const labels: { at: number; text: string }[] = []
  if (hi - lo <= DAY + HOUR) {
    for (let t = lo; t <= hi; t += 3 * HOUR) labels.push({ at: t, text: hhmm(t, tz) })
  } else {
    const days = Math.round((hi - lo) / DAY)
    const every = days <= 7 ? 1 : 5
    for (let i = 0; i < days; i += every) labels.push({ at: lo + i * DAY, text: shortDay(lo + i * DAY + 12 * HOUR, tz) })
  }

  // The strip is found from the event, so nothing reads a ref while rendering.
  const stripOf = (e: PointerEvent) => (e.currentTarget as HTMLElement).closest<HTMLElement>('.strip')!
  const px = (e: PointerEvent) => {
    const r = stripOf(e).getBoundingClientRect()
    return { x: e.clientX - r.left, width: r.width }
  }
  const begin = (e: PointerEvent) => {
    e.stopPropagation()
    const mode = (e.currentTarget as HTMLElement).dataset.mode as Drag['mode']
    stripOf(e).setPointerCapture(e.pointerId)
    setDrag({ mode, startX: e.clientX, start: win, win, moved: false })
  }
  const down = (e: PointerEvent) => {
    if (e.target !== e.currentTarget && !(e.target as HTMLElement).dataset.bg) return
    stripOf(e).setPointerCapture(e.pointerId)
    setDrag({ mode: 'click', startX: e.clientX, start: win, win, moved: false })
  }
  const move = (e: PointerEvent) => {
    const drag = dragRef.current
    if (!drag || drag.mode === 'click') return
    const { width } = px(e)
    const dx = ((e.clientX - drag.startX) / width) * (hi - lo)
    const next = drag.mode === 'move' ? moveWindow(drag.start, dx, loI, hiI) : resizeWindow(drag.start, drag.mode, dx, loI, hiI)
    setDrag({ ...drag, win: next, moved: Math.abs(e.clientX - drag.startX) > 2 })
  }
  const up = (e: PointerEvent) => {
    const drag = dragRef.current
    if (!drag) return
    stripOf(e).releasePointerCapture(e.pointerId)
    let final = drag.win
    if (drag.mode === 'click') {
      const { x, width } = px(e)
      final = centreWindow(win, lo + (x / width) * (hi - lo), lo, hi)
    }
    setDrag(null)
    if (final.from !== win.from || final.to !== win.to) onChange(selectionFor(final, nowMs))
  }
  const key = (e: KeyboardEvent) => {
    const step = Math.max(MINUTE, (win.to - win.from) / 10)
    const dir = e.key === 'ArrowLeft' ? -1 : e.key === 'ArrowRight' ? 1 : 0
    if (!dir) return
    e.preventDefault()
    const next = e.shiftKey ? resizeWindow(win, 'from', dir * step, loI, hiI) : moveWindow(win, dir * step, loI, hiI)
    onChange(selectionFor(next, nowMs))
  }

  return (
    <section aria-label="Overview" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 16px' }}>
        <span className="label">Overview</span>
        <div className="seg small" role="group" aria-label="Overview range">
          {spans.map(([k, label]) => (
            <button key={k} type="button" aria-pressed={span === k} onClick={() => setSpan(k)}>{label}</button>
          ))}
        </div>
        <span className="muted" style={{ fontSize: 12 }}>
          Runs per {data.bucketSeconds >= 3 * 3600 ? `${data.bucketSeconds / 3600} hours` : 'hour'}, failures in red. Kept for {data.retentionDays} days
          {data.watchedSince && Date.parse(data.watchedSince) > lo ? `; before ${shortDay(data.watchedSince, tz)} ${hhmm(data.watchedSince, tz)} OpsTide was not watching.` : '.'}
        </span>
      </div>
      <div style={{ position: 'relative' }}>
        <div className="strip-days" aria-hidden="true">
          {labels.map((l) => (
            <span key={l.at} className="mono" style={{ left: pct(l.at) }}>{l.text}</span>
          ))}
        </div>
        <div className="strip" onPointerDown={down} onPointerMove={move} onPointerUp={up} onPointerCancel={() => setDrag(null)}>
          {watched > lo && <div className="before" data-bg="1" style={{ width: widthPct(lo, watched) }} />}
          {data.gaps.map((g) => (
            <div key={g.from} className="gapband" data-bg="1" title={`Not watching ${shortDay(g.from, tz)} ${hhmm(g.from, tz)}–${hhmm(g.to, tz)}`}
              style={{ left: pct(Date.parse(g.from)), width: widthPct(Date.parse(g.from), Date.parse(g.to)) }} />
          ))}
          {labels.length > 1 && hi - lo > DAY + HOUR && labels.map((l) => <div key={l.at} className="rule" data-bg="1" style={{ left: pct(l.at) }} />)}
          {data.buckets.map((b) => {
            const start = Date.parse(b.start)
            const h = (b.runs / max) * 64
            const f = ((b.failed ?? 0) / max) * 64
            const title = `${shortDay(b.start, tz)} ${hhmm(b.start, tz)} · ${b.runs} runs${b.failed ? `, ${b.failed} failed` : ''}`
            const style = { left: pct(start), width: `calc(${widthPct(start, start + bucketMs)} - 0.5px)` }
            return (
              <span key={b.start} data-bg="1">
                <span className="sbar" data-bg="1" title={title} style={{ ...style, height: h }} />
                {f > 0 && <span className="sbar f" data-bg="1" title={title} style={{ ...style, height: f, bottom: h - f }} />}
              </span>
            )
          })}
          {nowMs >= lo && nowMs <= hi && <div className="nowline" data-bg="1" style={{ left: pct(nowMs) }} />}
          {!outside && (
          <div role="slider" tabIndex={0} aria-label="Visible window" aria-valuetext={`${shortDay(shown.from, tz)} ${hhmm(shown.from, tz)} to ${shortDay(shown.to, tz)} ${hhmm(shown.to, tz)}`}
            aria-valuemin={lo} aria-valuemax={hi} aria-valuenow={shown.from} onKeyDown={key} className="win" data-mode="move"
            style={{ left: pct(shown.from), width: widthPct(shown.from, shown.to) }} onPointerDown={begin}>
            <i className="l" data-mode="from" onPointerDown={begin} />
            <i className="r" data-mode="to" onPointerDown={begin} />
          </div>
          )}
        </div>
      </div>
      {outside && <span className="muted" style={{ fontSize: 12 }}>The window you are looking at is {win.to <= lo ? 'before' : 'after'} this strip. Click the strip to bring it here, or pick a longer span.</span>}
    </section>
  )
}
