import { common } from './tr-common'
import { explorer } from './tr-explorer'
import { guide } from './tr-guide'
import { images } from './tr-images'
import { schedules } from './tr-schedules'
import { server } from './tr-server'
import { workloads } from './tr-workloads'

// One table; the parts are split by screen so they can be read and kept next to the screens they cover.
export const tr: Record<string, string> = { ...common, ...explorer, ...workloads, ...schedules, ...images, ...guide, ...server }
