import type { CSSProperties, ReactNode } from 'react'

// One set of stroke icons for the whole interface: a 24-unit box, a 2-unit stroke, round caps and joins, drawn in the
// current text colour. An icon that only decorates a word is hidden from screen readers; one that carries meaning on
// its own (a row's kind, an icon button) is given a label, here or by its button.

export interface IconProps {
  size?: number
  /** Names the icon for a screen reader; without it the icon is decorative. */
  label?: string
  className?: string
  style?: CSSProperties
}

function Svg({ size = 16, label, className, style, children }: IconProps & { children: ReactNode }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round"
      className={`icon${className ? ` ${className}` : ''}`} style={style} focusable="false"
      {...(label ? { role: 'img', 'aria-label': label } : { 'aria-hidden': true })}>
      {label && <title>{label}</title>}
      {children}
    </svg>
  )
}

const icon = (paths: ReactNode) => function Icon(p: IconProps) {
  return <Svg {...p}>{paths}</Svg>
}

export const SearchIcon = icon(<><circle cx="11" cy="11" r="7" /><path d="M20 20l-4-4" /></>)
export const ClockIcon = icon(<><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>)
export const ChevronDown = icon(<path d="M6 9l6 6 6-6" />)
export const ChevronRight = icon(<path d="M9 6l6 6-6 6" />)
export const CloseIcon = icon(<path d="M6 6l12 12M18 6L6 18" />)
export const PlusIcon = icon(<path d="M12 5v14M5 12h14" />)
export const CheckIcon = icon(<path d="M5 12.5l4.5 4.5L19 7.5" />)
export const LayersIcon = icon(<><path d="M12 3l9 5-9 5-9-5 9-5z" /><path d="M3 13l9 5 9-5" /></>)
export const DownloadIcon = icon(<path d="M12 4v11M7 10l5 5 5-5M5 20h14" />)
export const LinkIcon = icon(<><path d="M10 14a4 4 0 0 0 6 0l3-3a4 4 0 0 0-6-6l-1 1" /><path d="M14 10a4 4 0 0 0-6 0l-3 3a4 4 0 0 0 6 6l1-1" /></>)
export const FilterIcon = icon(<path d="M4 5h16l-6 7.5V19l-4 1.5v-8z" />)
export const InfoIcon = icon(<><circle cx="12" cy="12" r="9" /><path d="M12 8h.01M11 12h1v4h1" /></>)
export const AlertIcon = icon(<><path d="M12 4l9 16H3z" /><path d="M12 10v4M12 17h.01" /></>)
export const ArchiveIcon = icon(<><path d="M4 5h16v4H4z" /><path d="M5 9v10h14V9M10 13h4" /></>)
export const FlagIcon = icon(<><path d="M5 21V4" /><path d="M5 4h11l-2 4 2 4H5" /></>)
export const PlugIcon = icon(<><path d="M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0z" /><path d="M12 17v4" /></>)

// Kinds of record.
export const CronJobIcon = icon(<><path d="M3 12a9 9 0 1 0 3-6.7" /><path d="M3 4v4h4M12 8v4l3 2" /></>)
export const DeploymentIcon = icon(<><path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z" /><path d="M12 12l8-4.5M12 12v9M12 12L4 7.5" /></>)
export const WorkloadIcon = icon(<><ellipse cx="12" cy="6" rx="8" ry="3" /><path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3" /></>)
export const ConfigIcon = icon(<><path d="M6 3h8l4 4v14H6z" /><path d="M14 3v4h4M9 12h6M9 16h6" /></>)
export const EventIcon = icon(<><path d="M6 16v-5a6 6 0 1 1 12 0v5l2 2H4z" /><path d="M10 21h4" /></>)
export const PodIcon = icon(<><circle cx="12" cy="12" r="8" /><circle cx="12" cy="12" r="3" /></>)
export const GapIcon = icon(<><path d="M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12z" /><path d="M4 4l16 16" /></>)
export const NodeIcon = icon(<><rect x="4" y="4" width="16" height="7" rx="1" /><rect x="4" y="13" width="16" height="7" rx="1" /><path d="M8 7.5h.01M8 16.5h.01" /></>)
export const ScalingIcon = icon(<><path d="M8 7l4-4 4 4M8 17l4 4 4-4M12 3v18" /></>)
export const WriteIcon = icon(<><path d="M4 20h4L19 9l-4-4L4 16z" /><path d="M13 7l4 4" /></>)
export const OtherIcon = icon(<circle cx="12" cy="12" r="3" />)

const byGroup: Record<string, (p: IconProps) => ReactNode> = {
  operations: CronJobIcon, releases: DeploymentIcon, workloads: WorkloadIcon, config: ConfigIcon, events: EventIcon,
  pods: PodIcon, observation: GapIcon, nodes: NodeIcon, scaling: ScalingIcon,
}

/** The icon of a record-type group (the facet list). */
export function GroupIcon({ group, ...p }: IconProps & { group: string }) {
  const I = byGroup[group] ?? OtherIcon
  return <I {...p} />
}

/** The icon of one record: a written spec is a pen, otherwise its group's. */
export function RecordIcon({ type, group, ...p }: IconProps & { type: string; group: string }) {
  if (type.endsWith('.spec_written')) return <WriteIcon {...p} />
  return <GroupIcon group={group} {...p} />
}
