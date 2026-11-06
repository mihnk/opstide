/** A short line with the fuller explanation one click, tap or key press away. The caveat stays available to everyone: it is
 *  a disclosure (details/summary), not a hover tooltip, so keyboard, touch and screen-reader users can open it. */
export function Note({ children, more, block }: { children: React.ReactNode; more: string; block?: boolean }) {
  return (
    <details className="note muted" style={{ display: block ? 'block' : 'inline-block', fontSize: 12, verticalAlign: 'top' }}>
      <summary style={{ cursor: 'pointer', display: 'inline' }}>{children} <span aria-hidden="true">ⓘ</span></summary>
      <span style={{ display: 'block', maxWidth: 560, paddingTop: 2 }}>{more}</span>
    </details>
  )
}
