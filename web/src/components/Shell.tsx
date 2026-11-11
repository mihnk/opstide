import { useState, type ReactNode } from 'react'
import { useAuth } from '../auth'
import { useLive } from '../live'
import { savedTheme, saveTheme, systemTheme, type Theme } from '../prefs'
import { Link } from '../router'
import { ago } from '../time'
import { useWorkspace } from '../workspace'
import { lang, setLang, t } from '../i18n'

const sections = [
  { href: '/', label: 'Overview', match: (p: string) => p === '/' },
  { href: '/explorer', label: 'Explorer', match: (p: string) => p.startsWith('/explorer') || p.startsWith('/workspace') },
  { href: '/workloads', label: 'Workloads', match: (p: string) => p.startsWith('/workloads') || p.startsWith('/nodes') },
  { href: '/releases', label: 'Releases', match: (p: string) => p.startsWith('/releases') },
  { href: '/schedules', label: 'Schedules', match: (p: string) => p.startsWith('/schedules') || p.startsWith('/operations') },
  { href: '/system', label: 'System', match: (p: string) => p.startsWith('/system') },
]

export function Shell({ path, cluster, demo, children }: { path: string; cluster?: string; demo?: boolean; children: ReactNode }) {
  const live = useLive()
  const auth = useAuth()
  const ws = useWorkspace()
  const [theme, setTheme] = useState<Theme>(() => savedTheme() ?? systemTheme())
  const other: Theme = theme === 'dark' ? 'light' : 'dark'

  // Freshness: the collector writes a heartbeat every 15 s.
  let status = t('connecting…')
  let stale = false
  if (live.lastWrite) {
    const age = live.clock - Date.parse(live.lastWrite)
    stale = age > 60_000
    const seen = ago(live.lastWrite, live.clock)
    status = stale ? t('no write for {age}', { age: seen.replace(/ ago$| önce$/, '') }) : t('last write {age}', { age: seen })
    if (live.synced === false) status = t('watches syncing · {status}', { status })
    else status = t('collector up · {status}', { status })
  }
  if (!live.connected && live.lastWrite) {
    status = t('live view offline · reload to retry')
    stale = true
  }

  return (
    <>
      {demo && (
        <div className="demo-banner" role="note">
          <b>{t('Demo.')}</b> {t('Made-up data, not a real cluster. Nothing is being collected; the history is a snapshot taken when this started.')}
        </div>
      )}
      <header className="masthead">
        <Link href="/" className="brand" style={{ color: 'var(--ink)', textDecoration: 'none' }}>OpsTide</Link>
        {cluster && <span className="mono muted" style={{ fontSize: 13 }}>{cluster}</span>}
        <nav aria-label={t('Sections')}>
          {sections.map((s) => (
            <Link key={s.href} href={s.href} aria-current={s.match(path) ? 'page' : undefined}>{t(s.label)}</Link>
          ))}
        </nav>
        <span className="spacer" />
        {ws.length > 0 && <Link href="/workspace" className="status mono" title={t('Records you picked, kept in this tab only')}>{t('workspace · {n}', { n: ws.length })}</Link>}
        <Link href="/system" className={`status mono${stale ? ' stale' : ''}`} title={t('Collector status and coverage')}>{status}</Link>
        <button type="button" className="theme-toggle" aria-label={other === 'light' ? t('Switch to the light theme') : t('Switch to the dark theme')}
          onClick={() => {
            saveTheme(other)
            setTheme(other)
          }}>
          {other === 'light' ? t('Light') : t('Dark')}
        </button>
        <button type="button" className="theme-toggle" lang={lang() === 'tr' ? 'en' : 'tr'}
          aria-label={lang() === 'tr' ? 'Switch to English' : 'Türkçeye geç'} onClick={() => setLang(lang() === 'tr' ? 'en' : 'tr')}>
          {lang() === 'tr' ? 'EN' : 'TR'}
        </button>
        {auth.required && <button type="button" className="theme-toggle" onClick={auth.logout}>{t('Sign out')}</button>}
      </header>
      <main>{children}</main>
    </>
  )
}

export function Failed({ error }: { error: Error }) {
  return <div className="error" role="alert">{t('Could not load this view:')} {error.message}</div>
}

export function Loading() {
  return <div className="empty" aria-busy="true">{t('Loading…')}</div>
}
