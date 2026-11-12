/** A short line with the fuller explanation one click, tap or key press away. The caveat stays available to everyone: it is
 *  a disclosure (details/summary), not a hover tooltip, so keyboard, touch and screen-reader users can open it. */
export function Note({ children, more, block }: { children: React.ReactNode; more: string; block?: boolean }) {
  return (
    <details className="note muted" style={{ display: block ? 'block' : 'inline-block' }}>
      <summary>{children} <span aria-hidden="true">ⓘ</span></summary>
      <span className="note-more">{more}</span>
    </details>
  )
}
