import { useState, type ReactNode } from 'react'
import { useAuth } from '../auth'
import { useLive } from '../live'
import { savedTheme, saveTheme, systemTheme, type Theme } from '../prefs'
import { Link } from '../router'
import { ago } from '../time'
import { useWorkspace } from '../workspace'
import { lang, setLang, t, type Lang } from '../i18n'
import { Choice } from './Choice'

const sections = [
  { href: '/', label: 'Overview', match: (p: string) => p === '/' },
  { href: '/explorer', label: 'Explorer', match: (p: string) => p.startsWith('/explorer') || p.startsWith('/workspace') },
  { href: '/workloads', label: 'Workloads', match: (p: string) => p.startsWith('/workloads') || p.startsWith('/nodes') },
  { href: '/images', label: 'Images', match: (p: string) => p.startsWith('/images') },
  { href: '/releases', label: 'Releases', match: (p: string) => p.startsWith('/releases') },
  { href: '/schedules', label: 'Schedules', match: (p: string) => p.startsWith('/schedules') || p.startsWith('/operations') },
  { href: '/system', label: 'System', match: (p: string) => p.startsWith('/system') },
]

export function Shell({ path, cluster, demo, children }: { path: string; cluster?: string; demo?: boolean; children: ReactNode }) {
  const live = useLive()
  const auth = useAuth()
  const ws = useWorkspace()
  const [theme, setTheme] = useState<Theme>(() => savedTheme() ?? systemTheme())

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
        <div className="tools">
          <ThemeChoice theme={theme} setTheme={setTheme} />
          <LangChoice />
          <Link href="/welcome" className="tool-btn" title={t('A short guide to OpsTide')} aria-current={path === '/welcome' ? 'page' : undefined}>
            <span className="tool-q" aria-hidden="true">?</span>{t('Guide')}
          </Link>
          {auth.required && <button type="button" className="tool-btn" onClick={auth.logout}>{t('Sign out')}</button>}
        </div>
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

/** Light (Fog) or dark (Night): both are shown, the one in use is filled. */
export function ThemeChoice({ theme, setTheme, size = 'tiny' }: { theme: Theme; setTheme: (t: Theme) => void; size?: 'tiny' | 'large' }) {
  return (
    <Choice label={t('Theme')} value={theme} size={size} marks={size === 'large'}
      onChange={(v) => { saveTheme(v); setTheme(v) }}
      options={[
        { value: 'light', name: t('Light'), icon: size === 'large' ? undefined : <SunIcon />, detail: size === 'large' ? 'Fog' : undefined },
        { value: 'dark', name: t('Dark'), icon: size === 'large' ? undefined : <MoonIcon />, detail: size === 'large' ? 'Night' : undefined },
      ]} />
  )
}

/** English or Turkish, each named in its own language. */
export function LangChoice({ size = 'tiny' }: { size?: 'tiny' | 'large' }) {
  const big = size === 'large'
  return (
    <Choice<Lang> label={t('Language')} value={lang()} size={size} marks={big} onChange={setLang}
      options={[
        { value: 'en', name: big ? 'English' : 'EN', full: big ? undefined : 'EN, English', lang: 'en' },
        { value: 'tr', name: big ? 'Türkçe' : 'TR', full: big ? undefined : 'TR, Türkçe', lang: 'tr' },
      ]} />
  )
}

function SunIcon() {
  return (
    <svg className="choice-icon" width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
      <circle cx="8" cy="8" r="3.2" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.4 1.4M11.6 11.6L13 13M3 13l1.4-1.4M11.6 4.4L13 3" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  )
}

function MoonIcon() {
  return (
    <svg className="choice-icon" width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
      <path d="M13.2 10.4A5.8 5.8 0 0 1 5.6 2.8a5.8 5.8 0 1 0 7.6 7.6z" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  )
}
