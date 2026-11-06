import type { HealthState, RolloutState } from '../api'

// Status is never colour alone: each state has its own shape and its own word.
// Rollout states are circles, squares and diamonds; health states are rounded
// squares and a triangle, so the two columns also differ in greyscale.

const rolloutWord: Record<RolloutState, string> = {
  observed: 'Observed', progressing: 'Progressing', completed: 'Completed', stalled: 'Stalled',
  failed: 'Failed', superseded: 'Superseded', unknown: 'Unknown',
}
const healthWord: Record<HealthState, string> = {
  still_observing: 'Still observing', healthy: 'Healthy', degraded: 'Degraded', insufficient_evidence: 'Insufficient evidence',
}

export const rolloutWords = rolloutWord
export const healthWords = healthWord

function Svg({ children, color }: { children: React.ReactNode; color: string }) {
  return (
    <svg className="glyph" width="12" height="12" viewBox="0 0 12 12" aria-hidden="true" style={{ color }}>
      {children}
    </svg>
  )
}

export function RolloutGlyph({ state }: { state: RolloutState }) {
  switch (state) {
    case 'completed':
      return <Svg color="var(--run)"><circle cx="6" cy="6" r="5" fill="currentColor" /><path d="M3.4 6.2l1.8 1.8 3.4-3.6" fill="none" stroke="var(--bg)" strokeWidth="1.4" /></Svg>
    case 'progressing':
      return <Svg color="var(--accent)"><circle cx="6" cy="6" r="4.6" fill="none" stroke="currentColor" strokeWidth="1.4" /><path d="M6 1.4a4.6 4.6 0 0 1 0 9.2z" fill="currentColor" /></Svg>
    case 'observed':
      return <Svg color="var(--accent)"><circle cx="6" cy="6" r="4.6" fill="none" stroke="currentColor" strokeWidth="1.4" /></Svg>
    case 'stalled':
      return <Svg color="var(--slow)"><rect x="1.5" y="1.5" width="9" height="9" fill="none" stroke="currentColor" strokeWidth="1.4" /><path d="M4.5 4v4M7.5 4v4" stroke="currentColor" strokeWidth="1.4" /></Svg>
    case 'failed':
      return <Svg color="var(--fail)"><path d="M6 .8L11.2 6 6 11.2.8 6z" fill="currentColor" /><path d="M4.4 4.4l3.2 3.2M7.6 4.4L4.4 7.6" stroke="var(--bg)" strokeWidth="1.3" /></Svg>
    case 'superseded':
      return <Svg color="var(--muted)"><circle cx="6" cy="6" r="4.6" fill="none" stroke="currentColor" strokeWidth="1.4" /><path d="M2.6 9.4l6.8-6.8" stroke="currentColor" strokeWidth="1.4" /></Svg>
    default:
      return <Svg color="var(--muted)"><circle cx="6" cy="6" r="4.6" fill="none" stroke="currentColor" strokeWidth="1.4" strokeDasharray="2 2" /></Svg>
  }
}

export function HealthGlyph({ health }: { health: HealthState }) {
  switch (health) {
    case 'healthy':
      return <Svg color="var(--run)"><rect x="1" y="1" width="10" height="10" rx="2.5" fill="currentColor" /><path d="M3.4 6.2l1.8 1.8 3.4-3.6" fill="none" stroke="var(--bg)" strokeWidth="1.4" /></Svg>
    case 'degraded':
      return <Svg color="var(--fail)"><path d="M6 1L11.2 10.6H.8z" fill="currentColor" /><path d="M6 4.6v3M6 8.8v.2" stroke="var(--bg)" strokeWidth="1.3" /></Svg>
    case 'still_observing':
      return <Svg color="var(--accent)"><rect x="1.5" y="1.5" width="9" height="9" rx="2.5" fill="none" stroke="currentColor" strokeWidth="1.4" /><path d="M6 3.4V6l1.8 1.2" fill="none" stroke="currentColor" strokeWidth="1.3" /></Svg>
    default:
      return <Svg color="var(--muted)"><rect x="1.5" y="1.5" width="9" height="9" rx="2.5" fill="none" stroke="currentColor" strokeWidth="1.4" strokeDasharray="2 2" /></Svg>
  }
}

const rolloutColor: Record<RolloutState, string> = {
  observed: 'var(--accent)', progressing: 'var(--accent)', completed: 'var(--ink)', stalled: 'var(--slow)', failed: 'var(--fail)', superseded: 'var(--muted)', unknown: 'var(--muted)',
}
const healthColor: Record<HealthState, string> = {
  still_observing: 'var(--ink)', healthy: 'var(--ink)', degraded: 'var(--fail)', insufficient_evidence: 'var(--muted)',
}

export function RolloutStatus({ state }: { state: RolloutState }) {
  return <span className="status-word" style={{ color: rolloutColor[state] }}><RolloutGlyph state={state} />{rolloutWord[state] ?? state}</span>
}

export function HealthStatus({ health }: { health: HealthState }) {
  return <span className="status-word" style={{ color: healthColor[health] }}><HealthGlyph health={health} />{healthWord[health] ?? health}</span>
}
