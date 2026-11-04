import { useEffect, useState, type AnchorHTMLAttributes, type MouseEvent } from 'react'

// A small history router: the pages are few and their state lives in the
// URL (path and query), so links can be shared.

const listeners = new Set<() => void>()

export function navigate(href: string, replace = false) {
  if (replace) history.replaceState(null, '', href)
  else history.pushState(null, '', href)
  listeners.forEach((l) => l())
  if (!replace) window.scrollTo(0, 0)
}

export function useLocation(): { path: string; params: URLSearchParams } {
  const [, setTick] = useState(0)
  useEffect(() => {
    const update = () => setTick((n) => n + 1)
    listeners.add(update)
    window.addEventListener('popstate', update)
    return () => {
      listeners.delete(update)
      window.removeEventListener('popstate', update)
    }
  }, [])
  return { path: location.pathname, params: new URLSearchParams(location.search) }
}

/** Replaces some query parameters, keeping the rest. */
export function withParams(params: URLSearchParams, change: Record<string, string | null>): string {
  const p = new URLSearchParams(params)
  for (const [k, v] of Object.entries(change)) {
    if (v === null || v === '') p.delete(k)
    else p.set(k, v)
  }
  const s = p.toString()
  return location.pathname + (s ? `?${s}` : '')
}

export function Link({ href, replace, onClick, ...rest }: AnchorHTMLAttributes<HTMLAnchorElement> & { href: string; replace?: boolean }) {
  const click = (e: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(e)
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
    if (!href.startsWith('/')) return
    e.preventDefault()
    navigate(href, replace)
  }
  return <a href={href} onClick={click} {...rest} />
}
