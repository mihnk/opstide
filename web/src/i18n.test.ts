import { afterEach, describe, expect, it } from 'vitest'
import type { Exception, Range } from './api'
import { t, tn, useLangForTest } from './i18n'
import { tr } from './i18n/tr'
import { exceptionText, rangeSentence } from './narrative'
import { ago, duration, longDay } from './time'

afterEach(() => useLangForTest('en'))

describe('interface language', () => {
  it('falls back to the English text, and fills values in', () => {
    useLangForTest('tr')
    expect(t('A phrase nobody translated')).toBe('A phrase nobody translated')
    expect(t('{n} failed', { n: 3 })).toBe('3 başarısız')
    expect(tn(1, '{n} gap', '{n} gaps')).toBe('1 boşluk')
    useLangForTest('en')
    expect(tn(2, '{n} gap', '{n} gaps')).toBe('2 gaps')
  })

  it('keeps every placeholder of the English text in the Turkish one', () => {
    const missing = Object.entries(tr).filter(([en, tk]) => {
      const names = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort().join(' ')
      return names(en) !== names(tk)
    })
    expect(missing).toEqual([])
  })

  it('builds the Overview sentences in Turkish', () => {
    useLangForTest('tr')
    const r = {
      from: '2026-11-06T00:00:00Z', to: '2026-11-07T00:00:00Z', now: '2026-11-06T14:55:00Z', gaps: [],
      counts: { observed: 12, succeeded: 10, failed: 2, running: 0, unknown: 0, missed: 0, skipped: 0, unconfirmed: 0, stillDue: 5 },
    } as unknown as Range
    expect(rangeSentence(r, 'UTC', true)).toBe('Saat 14:55 itibarıyla OpsTide 12 çalışma gördü: 10 başarılı ve 2 başarısız. Gece yarısına kadar 5 çalışma daha bekleniyor.')
    const e = { kind: 'failed', at: '2026-11-06T10:04:00Z', scheduledAt: '2026-11-06T10:00:00Z', count: 1, attempts: 3, reason: 'BackoffLimitExceeded', name: 'Reconcile ledger' } as unknown as Exception
    const x = exceptionText(e, 'UTC', r.from)
    expect(x.title).toBe('Reconcile ledger başarısız oldu')
    expect(x.detail).toBe('10:00 çalışması 3 denemeden sonra (BackoffLimitExceeded) başarısız oldu.')
  })

  it('writes dates and lengths in Turkish', () => {
    useLangForTest('tr')
    expect(longDay('2026-11-06T12:00:00Z', 'UTC')).toBe('6 Kasım Cuma')
    expect(duration(937)).toBe('15dk 37sn')
    expect(ago('2026-11-06T12:00:00Z', Date.parse('2026-11-06T12:03:00Z'))).toBe('3 dk önce')
  })
})
