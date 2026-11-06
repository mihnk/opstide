import { addToWorkspace, removeFromWorkspace, useWorkspace, WORKSPACE_MAX } from '../workspace'

/** Adds a record to, or takes it out of, the Investigation Workspace (kept in this tab only). */
export function WorkspaceToggle({ id, compact }: { id: string; compact?: boolean }) {
  const ids = useWorkspace()
  const has = ids.includes(id)
  const full = !has && ids.length >= WORKSPACE_MAX
  return (
    <button type="button" className={compact ? 'ws-toggle' : 'button'} aria-pressed={has} disabled={full}
      title={full ? `The Workspace holds at most ${WORKSPACE_MAX} records` : has ? 'Remove from the Investigation Workspace' : 'Add to the Investigation Workspace (kept in this tab only)'}
      onClick={(e) => {
        e.stopPropagation()
        if (has) removeFromWorkspace([id])
        else addToWorkspace([id])
      }}>
      {compact ? (has ? '✓' : '+') : has ? 'In Workspace · remove' : 'Add to Workspace'}
      {compact && <span className="sr">{has ? 'Remove from the Workspace' : 'Add to the Workspace'}</span>}
    </button>
  )
}
