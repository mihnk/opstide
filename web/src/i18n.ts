// The interface language. The words OpsTide draws itself (menus, headings, labels, buttons, sentences it builds)
// follow the reader's choice; what comes from the cluster or the collector (Kubernetes Event messages, entry
// summaries, error reasons) stays as recorded, in English.
//
// The English text is the key: t('Exceptions') looks the phrase up in the Turkish table and falls back to the
// English when there is no entry, so a missing translation shows English, never a blank or a key. Values are put in
// with {name}: t('{n} failed runs', { n: 2 }). The language is read once at start; changing it reloads the page.

import { tr } from './i18n/tr'

export type Lang = 'en' | 'tr'

const KEY = 'opstide.lang'

function stored(): Lang | null {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'en' || v === 'tr' ? v : null
  } catch {
    return null
  }
}

function initial(): Lang {
  const s = stored()
  if (s) return s
  const nav = typeof navigator !== 'undefined' ? navigator.language ?? '' : ''
  return nav.toLowerCase().startsWith('tr') ? 'tr' : 'en'
}

let current: Lang = initial()

export function lang(): Lang {
  return current
}

/** The locale for dates and numbers drawn in the chosen language. */
export function locale(): string {
  return current === 'tr' ? 'tr-TR' : 'en-GB'
}

/** Switches the language. When the choice can be kept it reloads, so every screen is drawn again in it; when storage is
 *  blocked (a private window) a reload would lose it, so the app is told to draw itself again instead. */
export function setLang(l: Lang) {
  if (l === current) return
  let kept = false
  try {
    localStorage.setItem(KEY, l)
    kept = localStorage.getItem(KEY) === l
  } catch {
    // not kept
  }
  current = l
  if (typeof document !== 'undefined') document.documentElement.lang = l
  if (kept && typeof location !== 'undefined') location.reload()
  else if (typeof window !== 'undefined') window.dispatchEvent(new Event(LANG_EVENT))
}

/** Fired when the language changed without a reload. */
export const LANG_EVENT = 'opstide-lang'

/** For tests. */
export function setLangForTest(l: Lang) {
  current = l
}

function fill(s: string, vars?: Record<string, string | number>): string {
  // One pass, so a value that itself holds "{name}" (an Event reason, say) is never filled again.
  return vars ? s.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m)) : s
}

export function t(text: string, vars?: Record<string, string | number>): string {
  return fill(current === 'tr' ? tr[text] ?? text : text, vars)
}

/** t() for an English word that means different things in different places: the Turkish table holds it under
 *  "context|text" ("pod|Scheduled" is when a Pod was placed on a Node, not a CronJob's schedule). */
export function tc(context: string, text: string, vars?: Record<string, string | number>): string {
  return fill(current === 'tr' ? tr[`${context}|${text}`] ?? tr[text] ?? text : text, vars)
}

/** t() with a singular and a plural English form; Turkish nouns after a number stay singular. */
export function tn(n: number, one: string, many: string, vars?: Record<string, string | number>): string {
  return t(n === 1 ? one : many, { n, ...vars })
}

if (typeof document !== 'undefined') document.documentElement.lang = current
