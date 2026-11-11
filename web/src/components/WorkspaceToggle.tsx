import { t } from '../i18n'
import { addToWorkspace, removeFromWorkspace, useWorkspace, WORKSPACE_MAX } from '../workspace'

/** Adds a record to, or takes it out of, the Investigation Workspace (kept in this tab only). */
export function WorkspaceToggle({ id, compact }: { id: string; compact?: boolean }) {
  const ids = useWorkspace()
  const has = ids.includes(id)
  const full = !has && ids.length >= WORKSPACE_MAX
  return (
    <button type="button" className={compact ? 'ws-toggle' : 'button'} aria-pressed={has} disabled={full}
      title={full ? t('The Workspace holds at most {max} records', { max: WORKSPACE_MAX }) : has ? t('Remove from the Investigation Workspace') : t('Add to the Investigation Workspace (kept in this tab only)')}
      onClick={(e) => {
        e.stopPropagation()
        if (has) removeFromWorkspace([id])
        else addToWorkspace([id])
      }}>
      {compact ? (has ? '✓' : '+') : has ? t('In Workspace · remove') : t('Add to Workspace')}
      {compact && <span className="sr">{has ? t('Remove from the Workspace') : t('Add to the Workspace')}</span>}
    </button>
  )
}
