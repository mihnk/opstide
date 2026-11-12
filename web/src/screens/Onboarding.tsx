import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { System } from '../api'
import { LangChoice, ThemeChoice } from '../components/Shell'
import { t, tn } from '../i18n'
import { useApi } from '../live'
import { savedTheme, savedZone, systemTheme, type Theme } from '../prefs'
import { navigate } from '../router'
import {
  ConnectionsArt, ExplorerArt, Figure, GapsArt, IntegrationsArt, NoCauseArt, OverviewArt, Pin, PointInTimeArt, ProvenanceArt,
  ReadOnlyArt, RecordArt, SchedulesArt, SystemArt, WatchingArt,
} from './OnboardingArt'

// The guide a browser sees first, in twelve short steps: a title, at most three short lines, and one picture each.
// It is shown once per browser (kept in local storage), stays reachable at /welcome (the header's Guide, and System),
// and the step is in the address (/welcome?step=3), so it can be resumed or shared. Arrow keys move between steps,
// Enter goes on, Escape leaves.

const KEY = 'opstide.onboarded'

export function onboarded(): boolean {
  try {
    return localStorage.getItem(KEY) === '1'
  } catch {
    // No storage (a private window): do not stand in the way every time.
    return true
  }
}

function markSeen() {
  try {
    localStorage.setItem(KEY, '1')
  } catch {
    // not kept; fine
  }
}

function leave() {
  markSeen()
  navigate('/')
}

type Chapter = 'Start' | 'Principles' | 'Evidence' | 'Screens' | 'Setup'
// English keys; drawn through t().
const steps: { key: string; chapter: Chapter; name: string; title: string }[] = [
  { key: 'welcome', chapter: 'Start', name: 'Welcome', title: 'OpsTide keeps the history of your cluster.' },
  { key: 'what', chapter: 'Principles', name: 'What it is', title: 'A record, not a remote control.' },
  { key: 'provenance', chapter: 'Evidence', name: 'How it is known', title: 'Every record says how it is known.' },
  { key: 'gaps', chapter: 'Evidence', name: 'Gaps', title: 'A quiet stretch is not a calm one.' },
  { key: 'connections', chapter: 'Evidence', name: 'Connections', title: 'Connection, not cause.' },
  { key: 'overview', chapter: 'Screens', name: 'Overview', title: 'Overview: the day at a glance.' },
  { key: 'explorer', chapter: 'Screens', name: 'Explorer', title: 'Explorer: every record, one list.' },
  { key: 'moment', chapter: 'Screens', name: 'Any moment', title: 'Workloads, Releases, Images: any moment.' },
  { key: 'schedules', chapter: 'Screens', name: 'Schedules', title: 'Schedules: every CronJob in one row.' },
  { key: 'system', chapter: 'Screens', name: 'System', title: 'System: what OpsTide can see.' },
  { key: 'integrations', chapter: 'Setup', name: 'Integrations', title: 'It fits what you already run.' },
  { key: 'watching', chapter: 'Setup', name: 'Watching now', title: 'Watching in this cluster now.' },
]
export const STEPS = steps.length

const chapters: Chapter[] = ['Start', 'Principles', 'Evidence', 'Screens', 'Setup']
const outline: [Chapter, string][] = [
  ['Principles', 'What it does, and never does'],
  ['Evidence', 'How to read a record'],
  ['Screens', 'A tour of each page'],
  ['Setup', 'Integrations, and what it watches here'],
]

/** The step in the address, 1-based; anything else is the first. */
function stepOf(params: URLSearchParams): number {
  const n = Number(params.get('step'))
  return Number.isInteger(n) && n >= 1 && n <= STEPS ? n : 1
}

const go = (n: number) => navigate(`/welcome?step=${n}`)

export function Onboarding({ params }: { params: URLSearchParams }) {
  const step = stepOf(params)
  const s = steps[step - 1]
  const { data: sys, error } = useApi<System>('/api/v1/system')
  const [theme, setTheme] = useState<Theme>(() => savedTheme() ?? systemTheme())
  const heading = useRef<HTMLHeadingElement>(null)
  const first = useRef(true)

  // A new step moves focus to its title, so a screen reader reads it and Tab starts from there; not on arrival.
  useEffect(() => {
    if (first.current) {
      first.current = false
      return
    }
    heading.current?.focus({ preventScroll: true })
  }, [step])

  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return
      const el = e.target instanceof Element ? e.target : null
      const typing = !!el?.closest('input, textarea, select, [contenteditable="true"]')
      const control = !!el?.closest('button, a, summary, [role="group"]')
      if (e.key === 'Escape') {
        e.preventDefault()
        leave()
      } else if (typing) {
        return
      } else if ((e.key === 'ArrowRight' || e.key === 'PageDown') && step < STEPS) {
        e.preventDefault()
        go(step + 1)
      } else if ((e.key === 'ArrowLeft' || e.key === 'PageUp') && step > 1) {
        e.preventDefault()
        go(step - 1)
      } else if (e.key === 'Enter' && !control) {
        e.preventDefault()
        if (step < STEPS) go(step + 1)
        else leave()
      }
    }
    window.addEventListener('keydown', key)
    return () => window.removeEventListener('keydown', key)
  }, [step])

  const tz = savedZone()
  const body = stepBody(s.key, { tz, sys, failed: !!error && !sys, theme, setTheme })
  const next = steps[step]

  return (
    <>
      <header className="masthead ob-mast">
        <span className="brand">OpsTide</span>
        {sys?.cluster && <span className="mono muted" style={{ fontSize: 14 }}>{sys.cluster}</span>}
        <span className="ob-mast-title">{t('Guide')}</span>
        <span className="spacer" />
        <div className="tools">
          {step > 1 && <ThemeChoice theme={theme} setTheme={setTheme} />}
          {step > 1 && <LangChoice />}
          <button type="button" className="tool-btn" onClick={leave}>{t('Skip the guide')}</button>
        </div>
      </header>
      <main className="ob">
        <Progress step={step} />
        <section key={step} className={`ob-stage${body.wide ? ' wide' : ''}${body.tour ? ' tour' : ''}`} aria-labelledby="ob-title">
          <div className="ob-text">
            <span className="label">{t(s.chapter)} · <span className="mono">{t('Step {n} of {total}', { n: step, total: STEPS })}</span></span>
            <h1 id="ob-title" ref={heading} tabIndex={-1}>{t(s.title)}</h1>
            {body.text}
          </div>
          <div className="ob-visual">{body.visual}</div>
        </section>
        <nav className="ob-nav" aria-label={t('Guide steps')}>
          <button type="button" className="ob-btn" disabled={step === 1} onClick={() => go(step - 1)}>
            <span aria-hidden="true">←</span> {t('Back')}
          </button>
          <span className="ob-keys" aria-hidden="true">
            <kbd>←</kbd><kbd>→</kbd> {t('move')} · <kbd>Enter</kbd> {t('next')} · <kbd>Esc</kbd> {t('leave')}
          </span>
          <span className="grow" />
          {next ? (
            <button type="button" className="ob-btn primary" onClick={() => go(step + 1)}>
              {t('Next: {name}', { name: t(next.name) })} <span aria-hidden="true">→</span>
            </button>
          ) : (
            <button type="button" className="ob-btn primary" onClick={leave}>
              {t('Open the Overview')} <span aria-hidden="true">→</span>
            </button>
          )}
        </nav>
      </main>
    </>
  )
}

/** The steps as a bar of segments under their chapters; each segment goes to its step. */
function Progress({ step }: { step: number }) {
  return (
    <nav className="ob-progress" aria-label={t('Guide progress')}>
      <div className="ob-chapters" aria-hidden="true">
        {chapters.map((c) => {
          const idx = steps.map((x, i) => (x.chapter === c ? i : -1)).filter((i) => i >= 0)
          const on = idx.includes(step - 1)
          return <span key={c} className={on ? 'on' : undefined} style={{ gridColumn: `${idx[0] + 1} / span ${idx.length}` }}>{t(c)}</span>
        })}
      </div>
      <ol className="ob-segs">
        {steps.map((x, i) => {
          const n = i + 1
          return (
            <li key={x.key}>
              <button type="button" className={n < step ? 'done' : n === step ? 'now' : undefined} aria-current={n === step ? 'step' : undefined}
                aria-label={t('Step {n} of {total}: {name}', { n, total: STEPS, name: t(x.name) })} title={t(x.name)} onClick={() => go(n)}>
                <i />
              </button>
            </li>
          )
        })}
      </ol>
    </nav>
  )
}

interface Ctx { tz: string; sys?: System; failed: boolean; theme: Theme; setTheme: (t: Theme) => void }

function Points({ items }: { items: ReactNode[] }) {
  return <ul className="ob-points">{items.map((x, i) => <li key={i}><span>{x}</span></li>)}</ul>
}

/** A numbered list that goes with the pins in the picture beside it. */
function Keyed({ items }: { items: [string, string][] }) {
  return (
    <ol className="ob-keyed">
      {items.map(([name, what], i) => <li key={name}><Pin n={i + 1} /><span><b>{t(name)}</b> <span className="muted">{t(what)}</span></span></li>)}
    </ol>
  )
}

function stepBody(key: string, c: Ctx): { text: ReactNode; visual: ReactNode; wide?: boolean; tour?: boolean } {
  switch (key) {
    case 'welcome':
      return {
        text: (
          <>
            <p className="ob-lead">{t('Twelve short steps, about three minutes. Leave whenever you like; the guide stays in the header.')}</p>
            <ol className="ob-outline">
              {outline.map(([ch, what]) => {
                const n = steps.filter((x) => x.chapter === ch).length
                return <li key={ch}><b>{t(ch)}</b><span className="muted">{t(what)}</span><span className="mono muted">{tn(n, '{n} step', '{n} steps')}</span></li>
              })}
            </ol>
            {c.sys?.demo && <p className="ob-note">{t('This is the demo: made-up data, not a real cluster.')}</p>}
          </>
        ),
        visual: (
          <div className="ob-settings">
            <div className="ob-setting">
              <span className="label" id="ob-lang">{t('Language')}</span>
              <LangChoice size="large" />
            </div>
            <div className="ob-setting">
              <span className="label">{t('Theme')}</span>
              <ThemeChoice theme={c.theme} setTheme={c.setTheme} size="large" />
            </div>
          </div>
        ),
      }
    case 'what':
      return {
        wide: true,
        text: <p className="ob-lead">{t('It keeps evidence. It does not act on it.')}</p>,
        visual: (
          <ul className="ob-three">
            <li><RecordArt /><b>{t('Records evidence')}</b><span>{t('What ran, changed and failed, with the time it happened.')}</span></li>
            <li><ReadOnlyArt /><b>{t('Never changes the cluster')}</b><span>{t('It only lists and watches. Nothing leaves the cluster.')}</span></li>
            <li><NoCauseArt /><b>{t('Never claims a cause')}</b><span>{t('It shows what came before. You decide why.')}</span></li>
          </ul>
        ),
      }
    case 'provenance':
      return {
        text: <Points items={[t('Each record carries one of three chips.'), t('A derived claim is made only when the record was continuous.'), t('Anything else is marked unknown, not guessed.')]} />,
        visual: <Figure><ProvenanceArt /></Figure>,
      }
    case 'gaps':
      return {
        text: <Points items={[t('The dashed band: OpsTide was not watching.'), t('Nothing in it is claimed: not a failure, not a missed run.'), t('Every gap is listed under System.')]} />,
        visual: <Figure><GapsArt /></Figure>,
      }
    case 'connections':
      return {
        text: <Points items={[t('Recorded before this lists earlier changes near a failure.'), t('Each says how it is connected: same resource, its owner, config it references, the Node it ran on.'), t('A connection is a lead to check, never a verdict.')]} />,
        visual: <Figure><ConnectionsArt /></Figure>,
      }
    case 'overview':
      return {
        text: <Keyed items={[
          ['Time window', 'Today, 24 h, 5 days, or your own.'],
          ['Five-day strip', 'Drag the box to move in time.'],
          ['Timetable', 'A lane per CronJob; bar width is run time.'],
          ['Legend', 'Shape and fill, not only colour.'],
          ['Exceptions', 'What needs a look, grouped.'],
          ['Maintenance', 'Declared planned work, as a band.'],
        ]} />,
        visual: <OverviewArt tz={c.tz} />,
        tour: true,
      }
    case 'explorer':
      return {
        text: <Keyed items={[
          ['Filters', 'Time, kind, namespace, text.'],
          ['A record', 'What happened, when, how it is known.'],
          ['Record panel', 'The facts, as recorded.'],
          ['Recorded before this', 'Earlier changes, and how they connect.'],
          ['Workspace', 'Pick records to compare, in this tab.'],
          ['Export JSON', 'A versioned file. Nothing is sent.'],
        ]} />,
        visual: <ExplorerArt tz={c.tz} />,
        tour: true,
      }
    case 'moment':
      return {
        text: <Points items={[
          <><b>{t('Workloads')}</b> {t('Deployments, StatefulSets and DaemonSets, with their revisions.')}</>,
          <><b>{t('Releases')}</b> {t('Each rollout, observed before it is called finished.')}</>,
          <><b>{t('Images')}</b> {t('What ran where. Pick any moment that is still kept.')}</>,
        ]} />,
        visual: <Figure><PointInTimeArt /></Figure>,
      }
    case 'schedules':
      return {
        text: <Points items={[
          <><b>{t('Last 7 days')}</b> {t('One square per day, shaped by its outcome.')}</>,
          <><b>{t('p50 · 7d')}</b> {t('The median run time of the last 7 days.')}</>,
          <><b>{t('Longer than its baseline')}</b> {t('At least 30% over the 23 days before. A marker, not an alarm.')}</>,
        ]} />,
        visual: <SchedulesArt tz={c.tz} />,
      }
    case 'system':
      return {
        text: <Points items={[
          <><b>{t('What is watched')}</b> {t('Every kind, and whether it is allowed.')}</>,
          <><b>{t('Gaps')}</b> {t('Each stretch OpsTide was not watching.')}</>,
          <><b>{t('Retention')}</b> {t('How far back the record goes.')}</>,
        ]} />,
        visual: <SystemArt sys={c.sys} />,
      }
    case 'integrations':
      return {
        text: <p className="ob-lead">{t('All optional, all set in the Helm values. OpsTide sends nothing out: the others read from it.')}</p>,
        visual: <IntegrationsArt sys={c.sys} />,
      }
    default: {
      const scope = c.sys ? (c.sys.namespaces.length ? c.sys.namespaces.join(', ') : t('the whole cluster')) : ''
      const kinds = c.sys ? new Set(c.sys.scopes.map((x) => x.kind)).size : 0
      return {
        text: (
          <>
            {c.sys && (
              <dl className="ob-facts">
                <div><dt className="label">{t('Cluster')}</dt><dd className="mono">{c.sys.cluster || '—'}</dd></div>
                <div><dt className="label">{t('Scope')}</dt><dd>{scope}{c.sys.synced ? '' : <span className="muted"> · {t('watches still syncing')}</span>}</dd></div>
                <div><dt className="label">{t('Kinds')}</dt><dd className="mono">{kinds}</dd></div>
              </dl>
            )}
            <p className="ob-lead">{t('That is all. The guide stays under Guide in the header.')}</p>
          </>
        ),
        visual: <Figure live={!!c.sys}><WatchingArt sys={c.sys} failed={c.failed} /></Figure>,
      }
    }
  }
}
