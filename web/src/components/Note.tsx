/** A short line with the fuller explanation one hover or focus away. The caveat stays; it just stops taking a paragraph. */
export function Note({ children, more, block }: { children: React.ReactNode; more: string; block?: boolean }) {
  return (
    <span className="muted note" title={more} tabIndex={0} aria-label={`${typeof children === 'string' ? children + '. ' : ''}${more}`} style={{ fontSize: 12, display: block ? 'block' : undefined, cursor: 'help' }}>
      {children} <span aria-hidden="true">ⓘ</span>
    </span>
  )
}
