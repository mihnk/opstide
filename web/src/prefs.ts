import { browserZone } from './time'

// Per-browser preferences. Storage can be unavailable (private windows);
// everything works without it.

function read(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function write(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key)
    else localStorage.setItem(key, value)
  } catch {
    // not kept; fine
  }
}

export type Theme = 'dark' | 'light'

export function savedTheme(): Theme | null {
  const t = read('opstide.theme')
  return t === 'dark' || t === 'light' ? t : null
}

export function systemTheme(): Theme {
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

export function applyTheme(t: Theme) {
  document.documentElement.dataset.theme = t
}

export function saveTheme(t: Theme) {
  write('opstide.theme', t)
  applyTheme(t)
}

export function savedZone(): string {
  const z = read('opstide.tz')
  if (z) {
    try {
      new Intl.DateTimeFormat('en', { timeZone: z })
      return z
    } catch {
      write('opstide.tz', null)
    }
  }
  return browserZone()
}

export function saveZone(z: string) {
  write('opstide.tz', z)
}
