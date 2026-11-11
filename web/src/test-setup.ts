import { beforeEach } from 'vitest'
import { useLangForTest } from './i18n'

// The tests read English; the machine's own language (LANG=tr_TR) must not change what they see.
beforeEach(() => useLangForTest('en'))
