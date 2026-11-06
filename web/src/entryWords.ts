/** The short label of a log entry's type. Prefixes cover whole families; an exact entry wins. */
export function entryWord(type: string, exact: Record<string, string> = {}): string {
  if (exact[type]) return exact[type]
  if (type.startsWith('statefulset.')) return type === 'statefulset.update_completed' ? 'Update' : 'StatefulSet'
  if (type.startsWith('daemonset.')) return type === 'daemonset.update_completed' ? 'Update' : 'DaemonSet'
  if (type.startsWith('node.')) return 'Node'
  return type
}
