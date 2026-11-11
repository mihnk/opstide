import { common } from './tr-common'
import { explorer } from './tr-explorer'
import { schedules } from './tr-schedules'
import { workloads } from './tr-workloads'

// One table; the parts are split by screen so they can be read and kept next to the screens they cover.
export const tr: Record<string, string> = { ...common, ...explorer, ...workloads, ...schedules }
