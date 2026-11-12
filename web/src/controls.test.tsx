// @vitest-environment jsdom
//
// The clock: a row of real buttons, one pressed. The arrow keys, Home and End move between them; pressing one chooses
// it. A zone chosen earlier that is neither the browser's nor UTC gets its own button, so it is never hidden.
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setLangForTest } from './i18n'
import { ZoneSelect } from './screens/common'

vi.mock('./time', async (orig) => ({ ...(await orig<typeof import('./time')>()), browserZone: () => 'Europe/Istanbul' }))

afterEach(() => {
  cleanup()
  setLangForTest('en')
})

function Clock({ start, compact, onSet }: { start: string; compact?: boolean; onSet?: (z: string) => void }) {
  const [tz, setTz] = useState(start)
  return <ZoneSelect tz={tz} setTz={(z) => { onSet?.(z); setTz(z) }} compact={compact} />
}

const pressed = () => screen.getAllByRole('button').filter((b) => b.getAttribute('aria-pressed') === 'true').map((b) => b.getAttribute('aria-label'))

describe('the clock', () => {
  it('offers the local time and UTC as buttons, with the chosen one pressed', () => {
    render(<Clock start="UTC" />)
    const group = screen.getByRole('group', { name: 'Clock' })
    const buttons = group.querySelectorAll('button')
    expect([...buttons].map((b) => b.getAttribute('aria-label'))).toEqual(['Local time: Europe/Istanbul, UTC+03', 'UTC'])
    expect([...buttons].map((b) => b.getAttribute('aria-pressed'))).toEqual(['false', 'true'])
    expect(buttons[0].textContent).toContain('Local time')
    expect(buttons[0].textContent).toContain('Europe/Istanbul UTC+03')
  })

  it('moves with the arrow keys, Home and End, and chooses on press', () => {
    const set = vi.fn()
    render(<Clock start="UTC" onSet={set} />)
    const [local, utc] = screen.getAllByRole('button')
    act(() => utc.focus())
    fireEvent.keyDown(utc, { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(local)
    fireEvent.keyDown(local, { key: 'ArrowLeft' })
    expect(document.activeElement).toBe(utc) // it wraps around
    fireEvent.keyDown(utc, { key: 'Home' })
    expect(document.activeElement).toBe(local)
    fireEvent.click(document.activeElement!)
    expect(set).toHaveBeenCalledWith('Europe/Istanbul')
    expect(pressed()).toEqual(['Local time: Europe/Istanbul, UTC+03'])
    fireEvent.keyDown(local, { key: 'End' })
    expect(document.activeElement).toBe(utc)
    // Pressing the chosen one again changes nothing.
    fireEvent.click(local)
    expect(set).toHaveBeenCalledTimes(1)
  })

  it('keeps a zone chosen earlier as a third button', () => {
    render(<Clock start="America/New_York" compact />)
    expect(screen.getAllByRole('button').map((b) => b.getAttribute('aria-pressed'))).toEqual(['false', 'false', 'true'])
    expect(pressed()[0]).toMatch(/^America\/New_York, UTC-0[45]$/)
  })

  it('says Yerel saat in Turkish', () => {
    setLangForTest('tr')
    render(<Clock start="UTC" />)
    expect(screen.getByRole('group', { name: 'Saat' }).textContent).toContain('Yerel saat')
  })
})
