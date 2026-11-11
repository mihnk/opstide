// @vitest-environment jsdom
//
// Every screen, drawn in English and in Turkish against answers recorded from `opstide -demo`
// (src/test/demo-api.json, made by web/scripts/record-ui-fixtures.cjs). A screen passes when, with every request
// answered, it has drawn its own heading, shows no alert, is not the "no page here" screen, and React logged nothing.
// This catches the class of bug where a field the screen did not expect (a null, a "not reported") brings a page down.
import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { AuthGate } from './auth'
import { setLangForTest, type Lang } from './i18n'
import { LiveProvider } from './live'
import fixtures from './test/demo-api.json'
import { addToWorkspace, clearWorkspace } from './workspace'

vi.setConfig({ testTimeout: 20_000 })

const api = fixtures.api as Record<string, unknown>
// Parameters that only move a window in time: the app computes them from "now", so they may differ from the
// recording. Any other difference is a request the real server was never asked, and fails the test.
const timeParams = new Set(['from', 'to', 'within', 'at', 'date', 'before', 'cursor'])
const strays: string[] = []
let inFlight = 0

function answer(url: string): unknown {
  const u = new URL(url, 'http://opstide.test')
  const exact = api[u.pathname + u.search]
  if (exact !== undefined) return exact
  const same = (k: string) => {
    const r = new URL(k, 'http://opstide.test')
    if (r.pathname !== u.pathname) return false
    const keys = new Set([...r.searchParams.keys(), ...u.searchParams.keys()])
    return [...keys].every((p) => timeParams.has(p) || r.searchParams.get(p) === u.searchParams.get(p))
  }
  const key = Object.keys(api).find(same)
  if (key === undefined) strays.push(u.pathname + u.search)
  return key === undefined ? undefined : api[key]
}

beforeAll(() => {
  // The recording's own "now", so what the screens compute from the clock does not drift as real time moves on.
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(new Date(fixtures.recordedAt))
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    inFlight++
    try {
      const body = answer(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url)
      return body === undefined
        ? new Response(JSON.stringify({ error: 'not recorded' }), { status: 404, headers: { 'Content-Type': 'application/json' } })
        : new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    } finally {
      // The body is read on the next turns; count the request as done only after them.
      setTimeout(() => inFlight--, 0)
    }
  })
  window.scrollTo = () => {}
  window.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia
})

afterAll(() => {
  vi.useRealTimers()
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  strays.length = 0
})

async function draw(path: string, l: Lang, opts: { onboarded?: boolean } = {}) {
  if (opts.onboarded === false) localStorage.removeItem('opstide.onboarded')
  else localStorage.setItem('opstide.onboarded', '1')
  localStorage.setItem('opstide.tz', 'UTC')
  setLangForTest(l)
  window.history.replaceState(null, '', path)
  const errors: string[] = []
  vi.spyOn(console, 'error').mockImplementation((...a: unknown[]) => { errors.push(a.map(String).join(' ').slice(0, 400)) })
  const onError = (e: ErrorEvent) => errors.push(String(e.error ?? e.message))
  window.addEventListener('error', onError)
  let view!: ReturnType<typeof render>
  try {
    await act(async () => {
      view = render(<AuthGate><LiveProvider><App /></LiveProvider></AuthGate>)
    })
    await waitFor(() => {
      expect(inFlight).toBe(0)
      const main = view.container.querySelector('main') ?? view.container
      expect(main.textContent ?? '').not.toMatch(/Loading…|Yükleniyor…|Checking…|Denetleniyor…/)
      expect(main.querySelector('h1, h2.title')).not.toBeNull()
    }, { timeout: 8000 })
    await act(async () => {})
  } finally {
    window.removeEventListener('error', onError)
  }
  const main = view.container.querySelector('main') ?? view.container
  return { view, main, errors }
}

function healthy(path: string, main: Element, errors: string[]) {
  expect(errors, `React logged errors on ${path}`).toEqual([])
  expect(strays, `requests on ${path} that were never recorded`).toEqual([])
  expect([...main.querySelectorAll('[role=alert]')].map((a) => a.textContent), `alerts on ${path}`).toEqual([])
  expect(main.textContent ?? '').not.toMatch(/There is no page here|Burada bir sayfa yok/)
}

describe.each(['en', 'tr'] as Lang[])('every screen in %s', (l) => {
  it.each(fixtures.pages)('%s', async (path) => {
    const { main, errors } = await draw(path, l)
    healthy(path, main, errors)
  })
})

describe('the introduction', () => {
  it('opens on a first visit', async () => {
    const { main, errors } = await draw('/', 'en', { onboarded: false })
    healthy('/', main, errors)
    expect(main.textContent).toContain('OpsTide keeps the history of your cluster.')
  })
  it('does not stand in front of a shared link', async () => {
    const { main, errors } = await draw('/?range=24h', 'en', { onboarded: false })
    healthy('/?range=24h', main, errors)
    expect(main.textContent).toContain('Scheduled operations')
  })
})

describe('the workspace with records in it', () => {
  it.each(['en', 'tr'] as Lang[])('%s', async (l) => {
    const ids = Object.keys(api).map((k) => /^\/api\/v1\/explorer\/records\/([^/?]+)$/.exec(k)?.[1]).filter((x): x is string => !!x).map(decodeURIComponent)
    expect(ids.length).toBeGreaterThan(5)
    addToWorkspace(ids)
    try {
      const { main, errors } = await draw('/workspace', l)
      healthy('/workspace', main, errors)
      await waitFor(() => expect(main.querySelectorAll('tbody tr').length).toBeGreaterThanOrEqual(ids.length))
    } finally {
      clearWorkspace()
    }
  })
})
