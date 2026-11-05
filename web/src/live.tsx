import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { getJSON } from './api'

// Live state from /api/v1/stream: a version that changes whenever the
// record does (pages refetch on it), the last write and whether the
// collector's watches are synced. Without the stream the pages still load;
// they just do not update by themselves.

interface Live {
  version: number
  lastWrite?: string
  synced?: boolean
  connected: boolean
  clock: number // now, in ms, ticking every 5 s for "4 s ago" texts
}

const LiveContext = createContext<Live>({ version: 0, connected: false, clock: 0 })

export function LiveProvider({ children }: { children: ReactNode }) {
  const [live, setLive] = useState<Live>(() => ({ version: 0, connected: false, clock: Date.now() }))
  const debounce = useRef<number | undefined>(undefined)

  useEffect(() => {
    if (typeof EventSource === 'undefined') return
    const es = new EventSource('/api/v1/stream')
    const ping = (e: MessageEvent) => {
      const d = JSON.parse(e.data) as { lastWrite?: string; synced?: boolean }
      setLive((l) => ({ ...l, lastWrite: d.lastWrite, synced: d.synced, connected: true }))
    }
    es.addEventListener('hello', () => setLive((l) => ({ ...l, connected: true })))
    es.addEventListener('ping', ping as EventListener)
    es.addEventListener('change', () => {
      // Several writes in a row become one refetch.
      window.clearTimeout(debounce.current)
      debounce.current = window.setTimeout(() => setLive((l) => ({ ...l, version: l.version + 1 })), 600)
    })
    es.onerror = () => setLive((l) => ({ ...l, connected: false }))
    return () => {
      window.clearTimeout(debounce.current)
      es.close()
    }
  }, [])

  useEffect(() => {
    const t = window.setInterval(() => setLive((l) => ({ ...l, clock: Date.now() })), 5000)
    return () => window.clearInterval(t)
  }, [])

  return <LiveContext.Provider value={live}>{children}</LiveContext.Provider>
}

export function useLive(): Live {
  return useContext(LiveContext)
}

type State<T> = { data?: T; error?: Error; loading: boolean }

/**
 * Fetches path and refetches it when the record changes. The latest
 * request wins: a slower earlier one is aborted. Data stays on screen
 * while a refetch runs.
 */
export function useApi<T>(path: string | null, opts: { keep?: boolean; refreshMs?: number } = {}): State<T> {
  const { version } = useLive()
  const [state, setState] = useState<State<T> & { path?: string | null }>({ loading: true })
  const [tick, setTick] = useState(0)
  const { keep, refreshMs } = opts

  // A window that follows now changes as time passes, not only when the record does.
  useEffect(() => {
    if (!refreshMs) return
    const t = window.setInterval(() => setTick((n) => n + 1), refreshMs)
    return () => window.clearInterval(t)
  }, [refreshMs])

  useEffect(() => {
    if (path === null) return
    const ctl = new AbortController()
    getJSON<T>(path, ctl.signal)
      .then((data) => setState({ data, loading: false, path }))
      .catch((error: Error) => {
        if (ctl.signal.aborted) return
        setState((s) => ({ data: s.path === path ? s.data : undefined, error, loading: false, path }))
      })
    return () => ctl.abort()
  }, [path, version, tick])

  // With keep, the last answer stays on screen while the next one loads, so
  // dragging a window does not blank the page or remount what is on it.
  if (state.path !== path) return { loading: true, data: keep ? state.data : undefined }
  return state
}
