import { useSyncExternalStore } from 'react'

// The Investigation Workspace is a list of record ids a person picked, kept in this browser tab (sessionStorage) and
// nowhere else. Nothing is written to the server: no accounts, no saved investigations. Closing the tab clears it.
// Only stable ids are kept; the records are read again when shown, so a record removed by retention shows up as missing.

const KEY = 'opstide.workspace.v1'
export const WORKSPACE_MAX = 500

interface Item { id: string; addedAt: number }

let items: Item[] = load()
const listeners = new Set<() => void>()
let snapshot: readonly string[] = items.map((i) => i.id)

function load(): Item[] {
  try {
    const raw = sessionStorage.getItem(KEY)
    if (!raw) return []
    const v = JSON.parse(raw) as unknown
    if (!Array.isArray(v)) return []
    const seen = new Set<string>()
    const out: Item[] = []
    for (const x of v) {
      const it = x as Item
      if (typeof it?.id === 'string' && /^(e\.\d{1,12}|k\.[A-Za-z0-9._:/=-]{1,200})$/.test(it.id) && !seen.has(it.id)) {
        seen.add(it.id)
        out.push({ id: it.id, addedAt: Number(it.addedAt) || 0 })
      }
    }
    return out.slice(0, WORKSPACE_MAX)
  } catch {
    return []
  }
}

function commit(next: Item[]) {
  items = next
  snapshot = items.map((i) => i.id)
  try {
    sessionStorage.setItem(KEY, JSON.stringify(items))
  } catch {
    // Storage can be unavailable; the list still works for this page view.
  }
  listeners.forEach((l) => l())
}

/** Adds ids, skipping ones already there. Returns how many were added and how many did not fit. */
export function addToWorkspace(ids: string[]): { added: number; full: number } {
  const have = new Set(items.map((i) => i.id))
  const next = [...items]
  let added = 0
  let full = 0
  for (const id of ids) {
    if (have.has(id)) continue
    if (next.length >= WORKSPACE_MAX) {
      full++
      continue
    }
    have.add(id)
    next.push({ id, addedAt: Date.now() })
    added++
  }
  if (added) commit(next)
  return { added, full }
}

export function removeFromWorkspace(ids: string[]) {
  const drop = new Set(ids)
  commit(items.filter((i) => !drop.has(i.id)))
}

export function clearWorkspace() {
  commit([])
}

export function useWorkspace(): readonly string[] {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => snapshot,
  )
}
