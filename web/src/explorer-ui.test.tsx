// @vitest-environment jsdom
//
// The Explorer's controls, one at a time: the search field's filter tokens (typed as key:value, removed with their
// button or with Backspace), "/" to search, a facet writing its filter into the link, the record table's arrow keys,
// the drawer's tabs, and the time range popover.
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useRef, useState } from 'react'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { parseToken, tokensOf, withToken, withoutToken, type ExplorerFilters, type ExplorerRecord } from './explorer'
import { setLangForTest } from './i18n'
import { navigate, withParams } from './router'
import { Facets } from './screens/ExplorerFacets'
import { RecordPanel } from './screens/ExplorerPanel'
import { SearchField, TimePicker } from './screens/ExplorerQuery'
import { RecordTable } from './screens/ExplorerTable'
import fixtures from './test/demo-api.json'

afterEach(() => {
  cleanup()
  setLangForTest('en')
  window.history.replaceState(null, '', '/explorer')
})

describe('filter tokens', () => {
  it('reads key:value in English and in Turkish, and refuses what is not a filter', () => {
    expect(parseToken('namespace:shop')).toEqual({ param: 'namespace', value: 'shop', rest: '' })
    expect(parseToken('redis outcome:Failed')).toEqual({ param: 'outcome', value: 'failed', rest: 'redis' })
    expect(parseToken('outcome:maybe')).toBeNull()
    expect(parseToken('colour:red')).toBeNull()
    setLangForTest('tr')
    expect(parseToken('sonuç:başarısız')).toEqual({ param: 'outcome', value: 'failed', rest: '' })
    expect(parseToken('köken:türetilmiş')).toEqual({ param: 'provenance', value: 'derived', rest: '' })
  })

  it('shows one token per value and adds or removes one value of a list', () => {
    const p = new URLSearchParams('namespace=shop&group=operations,events&q=x&range=6h')
    expect(tokensOf(p).map((x) => `${x.param}=${x.value}`)).toEqual(['namespace=shop', 'group=operations', 'group=events'])
    expect(withToken(p, 'group', 'pods')).toEqual({ group: 'operations,events,pods' })
    expect(withToken(p, 'namespace', 'data')).toEqual({ namespace: 'data' })
    expect(withoutToken(p, 'group', 'operations')).toEqual({ group: 'events' })
    expect(withoutToken(p, 'namespace', 'shop')).toEqual({ namespace: null })
  })
})

function Search({ start, onSet }: { start: string; onSet: (c: Record<string, string | null>) => void }) {
  const [params, setParams] = useState(new URLSearchParams(start))
  const ref = useRef<HTMLInputElement>(null)
  return (
    <>
      <button type="button">elsewhere</button>
      <SearchField params={params} inputRef={ref} set={(c) => { onSet(c); setParams(new URLSearchParams(withParams(params, c).split('?')[1] ?? '')) }} />
    </>
  )
}

describe('the search field', () => {
  it('adds a token for key:value on Enter and keeps the rest as text', () => {
    const set = vi.fn()
    render(<Search start="range=24h" onSet={set} />)
    const input = screen.getByRole('searchbox', { name: 'Search the records' })
    fireEvent.change(input, { target: { value: 'namespace:shop' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(set).toHaveBeenLastCalledWith({ namespace: 'shop', q: null })
    expect((input as HTMLInputElement).value).toBe('')
    expect(screen.getByRole('list', { name: '1 active filter' }).textContent).toContain('namespace:shop')

    fireEvent.change(input, { target: { value: 'oom outcome:failed' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(set).toHaveBeenLastCalledWith({ outcome: 'failed', q: 'oom' })
    expect((input as HTMLInputElement).value).toBe('oom')
  })

  it('says what a key takes when the value is not one of them', () => {
    const set = vi.fn()
    render(<Search start="" onSet={set} />)
    const input = screen.getByRole('searchbox')
    fireEvent.change(input, { target: { value: 'provenance:guessed' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    expect(set).not.toHaveBeenCalled()
    expect(screen.getByRole('status').textContent).toBe('provenance takes: observed, derived, unknown')
  })

  it('removes a token with its button, and the last one with Backspace in an empty field', () => {
    const set = vi.fn()
    render(<Search start="namespace=shop&severity=warning,error" onSet={set} />)
    fireEvent.click(screen.getByRole('button', { name: 'Remove the filter severity warning' }))
    expect(set).toHaveBeenLastCalledWith({ severity: 'error' })
    const input = screen.getByRole('searchbox')
    fireEvent.keyDown(input, { key: 'Backspace' })
    expect(set).toHaveBeenLastCalledWith({ severity: null })
    fireEvent.keyDown(input, { key: 'Backspace' })
    expect(set).toHaveBeenLastCalledWith({ namespace: null })
    // Backspace with text in the field edits the text.
    set.mockClear()
    fireEvent.change(input, { target: { value: 'a' } })
    fireEvent.keyDown(input, { key: 'Backspace' })
    expect(set).not.toHaveBeenCalled()
  })

  it('takes the focus on "/" from anywhere but another field', () => {
    render(<Search start="" onSet={() => {}} />)
    const input = screen.getByRole('searchbox')
    screen.getByRole('button', { name: 'elsewhere' }).focus()
    fireEvent.keyDown(document.activeElement!, { key: '/' })
    expect(document.activeElement).toBe(input)
    fireEvent.change(input, { target: { value: 'a/b' } })
    const other = document.createElement('input')
    document.body.appendChild(other)
    other.focus()
    fireEvent.keyDown(other, { key: '/' })
    expect(document.activeElement).toBe(other)
    other.remove()
  })
})

const facets: ExplorerFilters = {
  schema: 1, from: '', to: '', quickRanges: [], total: 10, counted: '',
  groups: [{ value: 'operations', count: 6 }, { value: 'events', count: 4 }, { value: 'nodes', count: 0 }],
  kinds: [{ value: 'Job', count: 6 }], provenance: [{ value: 'observed', count: 8 }, { value: 'derived', count: 2 }], sources: ['kubernetes.api'],
  reasons: [], namespaces: [{ value: 'shop', count: 7 }, { value: 'payments', count: 3 }],
  outcomes: [{ value: 'failed', count: 1 }, { value: 'succeeded', count: 5 }, { value: 'unknown', count: 0 }],
  severity: [{ value: 'error', count: 1 }, { value: 'warning', count: 2 }, { value: 'info', count: 7 }], owners: [],
}

describe('the facets', () => {
  it('write the filter into the link, and a value no record carries cannot be picked', () => {
    window.history.replaceState(null, '', '/explorer?range=6h&group=events')
    const set = (c: Record<string, string | null>) => navigate(withParams(new URLSearchParams(location.search), c), true)
    const { rerender } = render(<Facets params={new URLSearchParams(location.search)} facets={facets} set={set} />)
    const q = () => new URLSearchParams(location.search)
    fireEvent.click(screen.getByRole('checkbox', { name: /CronJob runs/ }))
    expect(q().get('group')).toBe('events,operations')
    rerender(<Facets params={q()} facets={facets} set={set} />)
    fireEvent.click(screen.getByRole('checkbox', { name: /payments/ }))
    expect(q().get('namespace')).toBe('payments')
    rerender(<Facets params={q()} facets={facets} set={set} />)
    fireEvent.click(screen.getByRole('checkbox', { name: /Kubernetes Event/ }))
    expect(q().get('group')).toBe('operations')
    expect(screen.getByRole('checkbox', { name: /^Node/ })).toHaveProperty('disabled', true)
    expect(q().get('range')).toBe('6h')
    rerender(<Facets params={q()} facets={facets} set={set} />)
    fireEvent.click(screen.getByRole('switch', { name: 'Changes only' }))
    expect(q().get('preset')).toBe('changes')
  })

  it('narrow to the values the search box matches', () => {
    render(<Facets params={new URLSearchParams()} facets={facets} set={() => {}} />)
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search filters' }), { target: { value: 'pay' } })
    expect(screen.getAllByRole('checkbox').map((c) => c.closest('label')!.textContent)).toEqual(['payments3'])
  })
})

const rec = (id: string, at: string): ExplorerRecord => ({
  id, type: 'job.failed', group: 'operations', source: 'opstide.rule', provenance: 'DERIVED', severity: 'error', summary: `run ${id}`,
  resource: { kind: 'Job', namespace: 'shop', name: id }, at, occurredAt: at, observedAt: at, refs: {},
})

describe('the record table', () => {
  it('moves between rows with the arrow keys, Home and End, and opens one with Enter', () => {
    const open = vi.fn()
    const rs = [rec('a', '2026-11-11T10:00:00Z'), rec('b', '2026-11-11T09:00:00Z'), rec('c', '2026-11-11T08:00:00Z')]
    render(<RecordTable records={rs} tz="UTC" rec="" onOpen={open} scopeLabel={(s) => s} label="Records" />)
    const rows = screen.getAllByRole('row').filter((r) => r.hasAttribute('data-row'))
    expect(rows.map((r) => r.tabIndex)).toEqual([0, -1, -1])
    act(() => rows[0].focus())
    fireEvent.keyDown(rows[0], { key: 'ArrowDown' })
    expect(document.activeElement).toBe(rows[1])
    fireEvent.keyDown(rows[1], { key: 'End' })
    expect(document.activeElement).toBe(rows[2])
    fireEvent.keyDown(rows[2], { key: 'ArrowUp' })
    expect(document.activeElement).toBe(rows[1])
    expect(rows.map((r) => r.tabIndex)).toEqual([-1, 0, -1])
    fireEvent.keyDown(rows[1], { key: 'Enter' })
    expect(open).toHaveBeenCalledWith('b')
    // The Workspace button inside a row does not open it.
    fireEvent.keyDown(rows[1].querySelector('button')!, { key: 'Enter' })
    expect(open).toHaveBeenCalledTimes(1)
  })

  it('marks the selected row', () => {
    render(<RecordTable records={[rec('a', '2026-11-11T10:00:00Z'), rec('b', '2026-11-11T09:00:00Z')]} tz="UTC" rec="b" onOpen={() => {}} scopeLabel={(s) => s} label="Records" />)
    const sel = screen.getAllByRole('row').filter((r) => r.getAttribute('aria-selected') === 'true')
    expect(sel.map((r) => r.getAttribute('data-row'))).toEqual(['b'])
    expect(sel[0].tabIndex).toBe(0)
  })
})

describe('the record drawer', () => {
  const api = fixtures.api as Record<string, unknown>
  const id = Object.keys(api).map((k) => /^\/api\/v1\/explorer\/records\/([^/?]+)\/before\?window=1h$/.exec(k)?.[1]).find((x): x is string => !!x)!
  beforeAll(() => {
    vi.stubGlobal('fetch', async (u: string) => {
      const body = api[new URL(u, 'http://x').pathname + new URL(u, 'http://x').search]
      return new Response(JSON.stringify(body ?? { error: 'not recorded' }), { status: body ? 200 : 404, headers: { 'Content-Type': 'application/json' } })
    })
  })
  afterAll(() => vi.unstubAllGlobals())

  it('opens a failure on "Before", with the count, and moves between tabs with the arrow keys', async () => {
    render(<RecordPanel id={decodeURIComponent(id)} tz="UTC" close={() => {}} />)
    const list = await screen.findByRole('tablist', { name: 'Record' })
    const tabs = () => screen.getAllByRole('tab')
    expect(tabs().map((x) => x.textContent?.replace(/\d+$/, ''))).toEqual(['Summary', 'Before', 'Relationships', 'Raw'])
    const before = () => tabs().find((x) => x.getAttribute('aria-selected') === 'true')!
    expect(before().id).toBe('dr-tab-before')
    await waitFor(() => expect(before().querySelector('.tab-badge')?.textContent).toMatch(/^\d+$/))
    expect(screen.getByRole('tabpanel', { name: 'Recorded before this' })).toBeDefined()

    act(() => before().focus())
    fireEvent.keyDown(list, { key: 'ArrowRight' })
    expect(document.activeElement?.id).toBe('dr-tab-relationships')
    expect(screen.getByRole('tabpanel').id).toBe('dr-panel-relationships')
    fireEvent.keyDown(list, { key: 'End' })
    expect(document.activeElement?.id).toBe('dr-tab-raw')
    fireEvent.keyDown(list, { key: 'ArrowRight' })
    expect(document.activeElement?.id).toBe('dr-tab-summary')
    expect(screen.getByRole('tabpanel').textContent).toContain('Observation')
    expect(tabs().map((x) => x.tabIndex)).toEqual([0, -1, -1, -1])
  })
})

function Picker() {
  const [params, setParams] = useState(new URLSearchParams('range=6h'))
  const [tz, setTz] = useState('UTC')
  return (
    <>
      <span>outside</span>
      <TimePicker params={params} set={(c) => setParams(new URLSearchParams(withParams(params, c).split('?')[1] ?? ''))} tz={tz} setTz={setTz}
        from="2026-11-11T04:00:00Z" to="2026-11-11T10:00:00Z" />
      <output>{params.toString()}</output>
    </>
  )
}

describe('the time range', () => {
  it('opens a dialog on the chosen range, closes on Escape with the focus back, and on a click outside', () => {
    render(<Picker />)
    const button = screen.getByRole('button', { name: /Last 6 hours/ })
    expect(button.getAttribute('aria-expanded')).toBe('false')
    fireEvent.click(button)
    const dialog = screen.getByRole('dialog', { name: 'Time range' })
    expect(button.getAttribute('aria-expanded')).toBe('true')
    expect(document.activeElement?.textContent).toBe('Last 6 hours6h')
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(button)
    expect(dialog.isConnected).toBe(false)

    fireEvent.click(button)
    fireEvent.mouseDown(screen.getByText('outside'))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('applies a quick range and a range by hand', () => {
    render(<Picker />)
    fireEvent.click(screen.getByRole('button', { name: /Last 6 hours/ }))
    fireEvent.click(screen.getByRole('button', { name: /Last 7 days/ }))
    expect(document.querySelector('output')!.textContent).toBe('range=7d')
    fireEvent.click(screen.getByRole('button', { name: /Last 7 days/ }))
    fireEvent.change(screen.getByLabelText('From (UTC)'), { target: { value: '2026-11-11T05:00' } })
    fireEvent.change(screen.getByLabelText('To (UTC)'), { target: { value: '2026-11-11T04:00' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    expect(screen.getByText('The end has to be after the start.')).toBeDefined()
    fireEvent.change(screen.getByLabelText('To (UTC)'), { target: { value: '2026-11-11T07:30' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    expect(document.querySelector('output')!.textContent).toBe('from=2026-11-11T05%3A00%3A00Z&to=2026-11-11T07%3A30%3A00Z')
  })
})
