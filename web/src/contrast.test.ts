// The colour pairs the interface draws text and controls with, measured in both themes (WCAG 2 contrast ratio):
// primary text at least 7:1, any other text at least 4.5:1, the edges of controls and the marks that carry meaning at
// least 3:1. A change to a token in styles.css that breaks one fails here.
import { describe, expect, it } from 'vitest'
import css from './styles.css?raw'

function tokens(theme: string): Record<string, string> {
  const at = css.search(new RegExp(`:root\\[data-theme=['"]${theme}['"]\\]\\s*\\{`))
  const body = css.slice(css.indexOf('{', at) + 1, css.indexOf('}', at))
  return Object.fromEntries([...body.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})/g)].map((m) => [m[1], m[2]]))
}

function luminance(hex: string): number {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255).map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(a: string, b: string): number {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p)
  return (x + 0.05) / (y + 0.05)
}

// [foreground, background, minimum, what]
const pairs: [string, string, number, string][] = [
  ['ink', 'bg', 7, 'primary text'],
  ['ink', 'sel', 7, 'text on a selected row or a hovered tile'],
  ['bg', 'ink', 7, 'the label of a pressed button'],
  ['muted', 'bg', 4.5, 'secondary text, zero counts'],
  ['muted', 'sel', 4.5, 'secondary text on a selected row'],
  ['accent', 'bg', 4.5, 'links'],
  ['accent', 'sel', 4.5, 'links on a selected row'],
  ['fail', 'bg', 4.5, 'failure counts and times'],
  ['fail', 'sel', 4.5, 'a failure on a selected row'],
  ['maint', 'bg', 4.5, 'the maintenance chip'],
  ['edge', 'bg', 3, 'the border of a control'],
  ['hatch', 'bg', 3, 'the gap hatch'],
  ['run', 'bg', 3, 'a succeeded mark'],
  ['slow', 'bg', 3, 'the "longer" marker and the warning glyph'],
]

describe.each([
  ['Night (dark)', 'dark'],
  ['Fog (light)', 'light'],
])('contrast in %s', (_, theme) => {
  const t = tokens(theme)
  it('finds the theme', () => expect(Object.keys(t).length).toBeGreaterThan(10))
  it.each(pairs)('%s on %s ≥ %s:1 (%s)', (fg, bg, min) => {
    expect(t[fg], `--${fg}`).toBeDefined()
    expect(t[bg], `--${bg}`).toBeDefined()
    expect(contrast(t[fg], t[bg])).toBeGreaterThanOrEqual(min)
  })
})
