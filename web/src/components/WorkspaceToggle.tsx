import { t } from '../i18n'
import { addToWorkspace, removeFromWorkspace, useWorkspace, WORKSPACE_MAX } from '../workspace'
import { CheckIcon, PlusIcon } from './icons'

/** Adds a record to, or takes it out of, the Investigation Workspace (kept in this tab only). `compact` is the small
 *  square beside a row; `icon` the icon button of a panel header; neither, a button with words. */
export function WorkspaceToggle({ id, compact, icon }: { id: string; compact?: boolean; icon?: boolean }) {
  const ids = useWorkspace()
  const has = ids.includes(id)
  const full = !has && ids.length >= WORKSPACE_MAX
  const tip = full ? t('The Workspace holds at most {max} records', { max: WORKSPACE_MAX }) : has ? t('Remove from the Investigation Workspace') : t('Add to the Investigation Workspace (kept in this tab only)')
  const words = has ? t('Remove from the Workspace') : t('Add to the Workspace')
  const glyph = has ? <CheckIcon size={icon ? 16 : 14} /> : <PlusIcon size={icon ? 16 : 14} />
  return (
    <button type="button" className={icon ? 'ibtn small' : compact ? 'ws-toggle' : 'button'} aria-pressed={has} disabled={full}
      {...(icon ? { 'data-tip': tip } : { title: tip })} aria-label={compact || icon ? words : undefined}
      onClick={(e) => {
        e.stopPropagation()
        if (has) removeFromWorkspace([id])
        else addToWorkspace([id])
      }}>
      {compact || icon ? glyph : has ? t('In Workspace · remove') : t('Add to Workspace')}
    </button>
  )
}
