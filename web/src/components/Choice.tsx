import { useRef, type KeyboardEvent, type ReactNode } from 'react'

// A row of buttons of which one is chosen: the clock, the theme, the language. Each option is a real button with
// aria-pressed, so it reads as "pressed" to a screen reader; the chosen one is filled and, with `marks`, carries a check
// where the others carry an empty ring, so the choice is seen without colour. Arrow keys, Home and End move between the
// options; Enter or Space chooses, as on any button.

export interface ChoiceOption<T extends string> {
  value: T
  name: ReactNode
  /** A second, quieter part of the label (a zone and its offset). */
  detail?: ReactNode
  /** The full words for a screen reader and the tooltip, when the visible label is short. */
  full?: string
  lang?: string
  icon?: ReactNode
}

export function Choice<T extends string>({ label, value, options, onChange, size = 'compact', marks, className, lead }: {
  label: string
  value: T
  options: ChoiceOption<T>[]
  onChange: (v: T) => void
  /** large: 44 px or more, for a primary place; compact: 36 px, beside other controls; tiny: the header; xs: 28 px,
   *  inside a panel. */
  size?: 'large' | 'compact' | 'tiny' | 'xs'
  marks?: boolean
  className?: string
  /** Drawn before the options, inside the group (an icon that says what the group is). */
  lead?: ReactNode
}) {
  const ref = useRef<HTMLDivElement>(null)
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    const buttons = [...(ref.current?.querySelectorAll('button') ?? [])]
    const i = buttons.indexOf(document.activeElement as HTMLButtonElement)
    if (i < 0) return
    let next = -1
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = (i + 1) % buttons.length
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') next = (i - 1 + buttons.length) % buttons.length
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = buttons.length - 1
    if (next < 0) return
    e.preventDefault()
    e.stopPropagation()
    buttons[next].focus()
  }
  return (
    <div ref={ref} role="group" aria-label={label} className={`choice ${size}${className ? ` ${className}` : ''}`} onKeyDown={key}>
      {lead}
      {options.map((o) => {
        const on = o.value === value
        return (
          <button key={o.value} type="button" aria-pressed={on} lang={o.lang} title={o.full} aria-label={o.full}
            onClick={() => { if (!on) onChange(o.value) }}>
            {marks && <Tick on={on} />}
            {o.icon}
            <span className="choice-name">{o.name}</span>
            {o.detail && <span className="choice-detail mono">{o.detail}</span>}
          </button>
        )
      })}
    </div>
  )
}

/** A check in a filled disc for the chosen option, an empty ring for the others. */
function Tick({ on }: { on: boolean }) {
  return (
    <svg className="tick" width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
      {on ? (
        <>
          <circle cx="8" cy="8" r="7.5" fill="currentColor" />
          <path d="M4.4 8.3l2.3 2.3 4.9-5.1" fill="none" stroke="var(--ink)" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" />
        </>
      ) : (
        <circle cx="8" cy="8" r="6.75" fill="none" stroke="currentColor" strokeWidth="1.5" />
      )}
    </svg>
  )
}
