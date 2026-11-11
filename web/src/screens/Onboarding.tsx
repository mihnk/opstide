import { getJSON, type System } from '../api'
import { lang, setLang, t, type Lang } from '../i18n'
import { kindName } from '../kinds'
import { Link, navigate } from '../router'
import { useEffect, useState } from 'react'

// The introduction a browser sees first: what OpsTide is, what it is watching in this cluster right now, and where
// to start. It is shown once per browser (kept in local storage) and stays reachable at /welcome, linked from System.

const KEY = 'opstide.onboarded'

export function onboarded(): boolean {
  try {
    return localStorage.getItem(KEY) === '1'
  } catch {
    // No storage (a private window): do not stand in the way every time.
    return true
  }
}

function finish() {
  try {
    localStorage.setItem(KEY, '1')
  } catch {
    // not kept; fine
  }
  navigate('/', true)
}

const places = [
  { href: '/', name: 'Overview', what: "Today's scheduled runs and what needs a look" },
  { href: '/explorer', name: 'Explorer', what: 'Every record, by time, kind and namespace' },
  { href: '/workloads', name: 'Workloads', what: 'Deployments, StatefulSets and DaemonSets with their revisions' },
  { href: '/system', name: 'System', what: 'What OpsTide can and cannot see' },
]

const principles = [
  { name: 'Records, not verdicts', what: 'Every entry is something Kubernetes reported or OpsTide saw. Findings are fixed rules, not a diagnosis.' },
  { name: 'Gaps are shown', what: 'When OpsTide was not watching, that time is hatched. A quiet stretch there means nothing.' },
  { name: 'Read-only', what: 'get, list and watch only. No Secrets, no logs, no exec; nothing leaves the cluster.' },
]

export function Onboarding() {
  const [sys, setSys] = useState<System>()
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    getJSON<System>('/api/v1/system').then(setSys).catch(() => setFailed(true))
  }, [])

  const kinds = sys ? [...new Map(sys.scopes.map((s) => [s.kind, sys.scopes.filter((x) => x.kind === s.kind)])).entries()] : []
  const scope = sys ? (sys.namespaces.length ? sys.namespaces.join(', ') : t('the whole cluster')) : ''

  return (
    <>
      <header className="masthead">
        <span className="brand">OpsTide</span>
        {sys?.cluster && <span className="mono muted" style={{ fontSize: 13 }}>{sys.cluster}</span>}
        <span className="spacer" />
        <div className="seg small" role="group" aria-label={t('Language')} style={{ marginBottom: 10 }}>
          {([['en', 'English'], ['tr', 'Türkçe']] as [Lang, string][]).map(([l, label]) => (
            <button key={l} type="button" lang={l} aria-pressed={lang() === l} onClick={() => setLang(l)}>{label}</button>
          ))}
        </div>
      </header>
      <main>
        <div className="page" style={{ maxWidth: 1080 }}>
          <section style={{ display: 'flex', flexDirection: 'column', gap: 10, paddingTop: 12 }}>
            <span className="label">{t('Welcome')}</span>
            <h1 className="title" style={{ fontSize: 44 }}>{t('OpsTide keeps the history of your cluster.')}</h1>
            <p className="sentence">{t('It watches with read-only access and records what changed, what ran and what failed, so you can go back to any moment. It does not judge and it never changes anything.')}</p>
            {sys?.demo && <p className="muted" style={{ margin: 0 }}>{t('This is the demo: made-up data, not a real cluster.')}</p>}
          </section>

          <section aria-label={t('Watching now')} className="rows">
            <h2 className="label section-title">{t('Watching now')}</h2>
            {failed && <div className="error" role="alert">{t('Could not reach the collector. The rest of this page still applies.')}</div>}
            {!sys && !failed && <div className="empty" aria-busy="true">{t('Loading…')}</div>}
            {sys && (
              <>
                <div className="row" style={{ gridTemplateColumns: '180px minmax(0, 1fr)' }}>
                  <span className="label">{t('Scope')}</span>
                  <span>{scope}{sys.synced ? '' : <span className="muted"> · {t('watches still syncing')}</span>}</span>
                </div>
                {kinds.length === 0 && <div className="empty">{t('No watch has started yet.')}</div>}
                {kinds.map(([kind, scopes]) => {
                  const problem = scopes.find((s) => s.problem)?.problem
                  return (
                    <div key={kind} className="row" style={{ gridTemplateColumns: '22px minmax(0, 1fr) auto' }}>
                      <span className="mono" aria-hidden="true" style={{ color: problem ? 'var(--fail)' : 'var(--run)' }}>{problem ? '✕' : '✓'}</span>
                      <span>{t(kindName[kind] ?? kind)}{problem && <span className="sub" style={{ fontSize: 12 }}>{problem}</span>}</span>
                      <span className="mono" style={{ fontSize: 12, color: problem ? 'var(--fail)' : 'var(--muted)' }}>
                        {problem ? (/forbidden/i.test(problem) ? t('not allowed') : t('failing')) : t('watching')}
                      </span>
                    </div>
                  )
                })}
              </>
            )}
          </section>

          <div className="two">
            <section aria-label={t('Three things to know')} className="rows">
              <h2 className="label section-title">{t('Three things to know')}</h2>
              {principles.map((p) => (
                <div key={p.name} className="row" style={{ gridTemplateColumns: 'minmax(0, 1fr)' }}>
                  <span><span style={{ fontWeight: 600 }}>{t(p.name)}</span><span className="sub">{t(p.what)}</span></span>
                </div>
              ))}
            </section>
            <section aria-label={t('Where to start')} className="rows">
              <h2 className="label section-title">{t('Where to start')}</h2>
              {places.map((p) => (
                <Link key={p.href} className="row" href={p.href} onClick={() => { try { localStorage.setItem(KEY, '1') } catch { /* fine */ } }}
                  style={{ gridTemplateColumns: '120px minmax(0, 1fr)' }}>
                  <span style={{ fontWeight: 600 }}>{t(p.name)}</span>
                  <span className="muted">{t(p.what)}</span>
                </Link>
              ))}
            </section>
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
            <button type="button" className="button" onClick={finish}
              style={{ height: 40, padding: '0 22px', background: 'var(--ink)', color: 'var(--bg)', fontWeight: 600 }}>{t('Get started')}</button>
            <span className="muted" style={{ fontSize: 13 }}>{t('This page stays under System › Introduction.')}</span>
          </div>
        </div>
      </main>
    </>
  )
}
