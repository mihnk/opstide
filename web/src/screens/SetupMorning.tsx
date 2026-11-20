import { useState } from 'react'
import { postJSON, query, type Delivery, type MorningPreview, type NotifyStatus } from '../api'
import { Loading } from '../components/Shell'
import { coversWords, destinationIcon, includeWords, MorningMessage, notifyValues, typeWords } from '../components/Morning'
import { lang, t } from '../i18n'
import { useApi } from '../live'
import { Link, navigate } from '../router'
import { relativeTime } from '../time'

type Kind = 'slack' | 'teams' | 'webhook'

/** The setup's choices, kept in the address so a reload or a shared link comes back to them. The URL is not: it is a
 *  secret, and stays in this page until the person copies the command. */
interface Choice {
  step: 1 | 2
  type: Kind
  name: string
  time: string // HH:MM
  days: 'weekdays' | 'daily'
  covers: string
  lang: 'en' | 'tr'
  include: string[]
}

function choiceOf(p: URLSearchParams): Choice {
  const type = (['slack', 'teams', 'webhook'] as const).find((k) => k === p.get('type')) ?? 'slack'
  const time = /^([01]\d|2[0-3]):[0-5]\d$/.test(p.get('time') ?? '') ? p.get('time')! : '08:30'
  const include = (p.get('include') ?? 'failures,troubles,gaps,due').split(',').filter((k) => includeWords.some(([w]) => w === k))
  return {
    step: p.get('step') === '2' ? 2 : 1,
    type,
    name: (p.get('name') ?? (type === 'webhook' ? 'ops-bot' : '#ops-morning')).slice(0, 80),
    time,
    days: p.get('days') === 'daily' ? 'daily' : 'weekdays',
    covers: coversWords[p.get('covers') ?? ''] ? p.get('covers')! : 'since-18h',
    lang: p.get('lang') === 'tr' || (!p.get('lang') && lang() === 'tr') ? 'tr' : 'en',
    include,
  }
}

function toParams(c: Choice): string {
  return query({ step: c.step === 2 ? 2 : undefined, type: c.type, name: c.name, time: c.time, days: c.days, covers: c.covers, lang: c.lang, include: c.include.join(',') })
}

const cron = (c: Choice) => {
  const [h, m] = c.time.split(':').map(Number)
  return `${m} ${h} * * ${c.days === 'weekdays' ? '1-5' : '*'}`
}

/** A URL in single quotes for a shell. */
const shellQuote = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`

export function SetupMorning({ params, tz }: { params: URLSearchParams; tz: string }) {
  const c = choiceOf(params)
  const set = (patch: Partial<Choice>) => navigate(`/setup/morning${toParams({ ...c, ...patch })}`, true)
  const { data: preview } = useApi<MorningPreview>(`/api/v1/morning${query({ tz, covers: c.covers, lang: c.lang, include: c.include.join(',') })}`)
  const at = `${c.days === 'weekdays' ? t('Weekdays') : t('Every day')} · ${c.time}`

  return (
    <>
      <header className="masthead ob-mast">
        <span className="brand">OpsTide</span>
        <span className="ob-mast-title">{t('Setup')}</span>
        <span className="spacer" />
        <div className="tools">
          <Link className="tool-btn" href="/system/integrations">{t('Not now')}</Link>
        </div>
      </header>
      <main className="ob">
        <ol className="setup-steps" aria-label={t('Setup steps')}>
          <li className="on" aria-current={c.step === 1 ? 'step' : undefined}><i />{t('1 · Where and when')}</li>
          <li className={c.step === 2 ? 'on' : undefined} aria-current={c.step === 2 ? 'step' : undefined}><i />{t('2 · Connect and test')}</li>
        </ol>
        <section className="ob-stage setup-stage" aria-labelledby="setup-title">
          <div className="ob-text">
            {c.step === 1 ? <StepWhere c={c} set={set} tz={tz} /> : <StepConnect c={c} tz={tz} />}
          </div>
          <div className="setup-aside">
            <span className="label">{c.step === 1 ? t('Preview · from this cluster\'s record') : t('What your channel will get')}</span>
            {preview ? <MorningMessage lines={preview.lines} channel={c.name} when={at} /> : <Loading />}
            <span className="muted fine">{t('Built from what OpsTide recorded, so you see what your team will read.')}</span>
          </div>
        </section>
      </main>
    </>
  )
}

function StepWhere({ c, set, tz }: { c: Choice; set: (p: Partial<Choice>) => void; tz: string }) {
  const toggle = (k: string) => set({ include: c.include.includes(k) ? c.include.filter((x) => x !== k) : includeWords.map(([w]) => w).filter((w) => w === k || c.include.includes(w)) })
  return (
    <>
      <span className="label">{t('Setup')} · <span className="mono">{t('Morning summary')}</span></span>
      <h1 id="setup-title">{t('Where should the morning summary go?')}</h1>
      <div className="pick-cards" role="radiogroup" aria-label={t('Destination')}>
        {(['slack', 'teams', 'webhook'] as const).map((k) => {
          const Icon = destinationIcon[k]
          return (
            <button key={k} type="button" role="radio" aria-checked={c.type === k} onClick={() => set({ type: k, name: k === 'webhook' ? 'ops-bot' : c.type === 'webhook' ? '#ops-morning' : c.name })}>
              <Icon /><b>{t(typeWords[k][0])}</b><span className="muted">{t(typeWords[k][1])}</span>
            </button>
          )
        })}
      </div>
      <div className="setup-form">
        <label className="label" htmlFor="setup-name">{c.type === 'webhook' ? t('Name') : t('Channel name')}</label>
        <input id="setup-name" className="input" value={c.name} maxLength={80} onChange={(e) => set({ name: e.target.value })} />
        <span className="label">{t('Send at')}</span>
        <div className="setup-inline">
          <input className="input mono" type="time" aria-label={t('Time')} value={c.time} onChange={(e) => e.target.value && set({ time: e.target.value })} style={{ width: 120 }} />
          <div className="seg small" role="group" aria-label={t('Days')}>
            <button type="button" aria-pressed={c.days === 'weekdays'} onClick={() => set({ days: 'weekdays' })}>{t('Weekdays')}</button>
            <button type="button" aria-pressed={c.days === 'daily'} onClick={() => set({ days: 'daily' })}>{t('Every day')}</button>
          </div>
          <span className="muted fine">{tz}</span>
        </div>
        <span className="label">{t('Covers')}</span>
        <div className="seg small" role="group" aria-label={t('Covers')}>
          {Object.entries(coversWords).map(([k, w]) => <button key={k} type="button" aria-pressed={c.covers === k} onClick={() => set({ covers: k })}>{t(w)}</button>)}
        </div>
        <span className="label">{t('Language')}</span>
        <div className="seg small" role="group" aria-label={t('Language')}>
          <button type="button" aria-pressed={c.lang === 'en'} onClick={() => set({ lang: 'en' })}>English</button>
          <button type="button" aria-pressed={c.lang === 'tr'} onClick={() => set({ lang: 'tr' })}>Türkçe</button>
        </div>
      </div>
      <fieldset className="setup-include">
        <legend className="label">{t('Include')}</legend>
        {includeWords.map(([k, w]) => (
          <label key={k} className="check-row"><input type="checkbox" checked={c.include.includes(k)} onChange={() => toggle(k)} /> {t(w)}</label>
        ))}
      </fieldset>
      <nav className="ob-nav setup-nav">
        <Link className="ob-btn" href="/system/integrations"><span aria-hidden="true">←</span> {t('Back')}</Link>
        <span className="grow" />
        <span className="muted fine">{t('You can change all of this later.')}</span>
        <button type="button" className="ob-btn primary" disabled={!c.name.trim()} onClick={() => set({ step: 2 })}>{t('Next: connect')} <span aria-hidden="true">→</span></button>
      </nav>
    </>
  )
}

function StepConnect({ c, tz }: { c: Choice; tz: string }) {
  const [url, setUrl] = useState('')
  const [copied, setCopied] = useState('')
  const [sent, setSent] = useState<Delivery | string | null>(null)
  const { data: st } = useApi<NotifyStatus>('/api/v1/notify', { refreshMs: 5000 })
  const found = st?.destinations.find((d) => d.name === c.name)
  const secret = 'opstide-notify'
  const key = c.type
  const looks = !url ? '' : c.type === 'slack' ? (/^https:\/\/hooks\.slack\.com\/services\//.test(url) ? 'ok' : 'bad')
    : c.type === 'teams' ? (/^https:\/\/\S+$/.test(url) ? 'ok' : 'bad') : (/^https?:\/\/\S+$/.test(url) ? 'ok' : 'bad')
  const command = (u: string, wrap: string) => `kubectl -n opstide create secret generic ${secret}${wrap}--from-literal=${key}=${shellQuote(u)}`
  const values = notifyValues({ schedule: cron(c), timeZone: tz, covers: c.covers, language: c.lang, include: c.include, destinations: [{ type: c.type, name: c.name, secret, key }] })
  const copy = async (what: string, text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(what)
    } catch {
      setCopied('')
    }
  }
  const download = () => {
    const a = document.createElement('a')
    a.href = URL.createObjectURL(new Blob([values + '\n'], { type: 'text/yaml' }))
    a.download = 'opstide-notify-values.yaml'
    a.click()
    URL.revokeObjectURL(a.href)
  }
  const test = async () => {
    setSent(null)
    try {
      setSent(await postJSON<Delivery>('/api/v1/notify/test', { destination: c.name }))
    } catch (e) {
      setSent(e instanceof Error ? e.message : String(e))
    }
  }
  return (
    <>
      <span className="label">{t('Setup')} · <span className="mono">{t(typeWords[c.type][0])} · {c.name}</span></span>
      <h1 id="setup-title">{t('Connect it, the way the cluster is run.')}</h1>
      <p className="ob-lead" style={{ margin: 0 }}>{t('The webhook URL is a secret: it goes into a Kubernetes Secret, and the schedule into the chart, so it is reviewed like any other change. OpsTide writes both for you.')}</p>
      <div className="setup-block">
        <label className="label" htmlFor="setup-url">{t('1 · Paste the webhook URL')} <span className="muted setup-hint">{t('it stays in this page; OpsTide never receives it')}</span></label>
        <div className="setup-inline">
          <input id="setup-url" className="input mono" style={{ flex: 1 }} type="password" autoComplete="off" spellCheck={false} value={url} onChange={(e) => setUrl(e.target.value.trim())} placeholder={c.type === 'slack' ? 'https://hooks.slack.com/services/…' : 'https://…'} />
          {looks === 'ok' && <span className="ok fine">{t('✓ looks right')}</span>}
          {looks === 'bad' && <span className="fail fine">{c.type === 'slack' ? t('a Slack webhook starts with https://hooks.slack.com/services/') : t('needs to be an https URL')}</span>}
        </div>
      </div>
      <div className="setup-block">
        <div className="setup-inline"><span className="label">{t('2 · Create the Secret')}</span><span className="grow" /><button type="button" className="button small" onClick={() => copy('secret', command(url || 'PASTE-THE-URL', ' '))}>{copied === 'secret' ? t('Copied') : t('Copy')}</button></div>
        <pre className="code-block">{command(url ? '•'.repeat(12) : 'PASTE-THE-URL', ' \\\n  ')}</pre>
        <span className="muted fine">{t('Shown hidden; Copy copies it with the URL. Use the namespace OpsTide runs in.')}</span>
      </div>
      <div className="setup-block">
        <div className="setup-inline"><span className="label">{t('3 · Add to your values and upgrade')}</span><span className="grow" />
          <button type="button" className="button small" onClick={() => copy('values', values)}>{copied === 'values' ? t('Copied') : t('Copy')}</button>
          <button type="button" className="button small" onClick={download}>{t('As a values file')}</button>
        </div>
        <pre className="code-block">{values}</pre>
      </div>
      <div className="note-box" role="status">
        {found ? (
          <><b>{t('Connected.')}</b> {t('OpsTide runs with {name}. Send a test to see it arrive.', { name: c.name })}{found.last && <span className="muted"> {t('Last: {when}', { when: relativeTime(found.last.at, new Date().toISOString(), tz) })}</span>}</>
        ) : (
          <><b>{t('Waiting for the upgrade.')}</b> {t('This page checks every few seconds. When OpsTide starts with this destination, you can send a test message from here.')} <span className="mono muted">{t('checking…')}</span></>
        )}
        {sent && <div className={typeof sent !== 'string' && sent.ok ? 'ok' : 'fail'}>{typeof sent === 'string' ? sent : sent.ok ? t('Test delivered (HTTP {status}).', { status: sent.status ?? 200 }) : sent.error ?? t('Test not delivered')}</div>}
      </div>
      <nav className="ob-nav setup-nav">
        <Link className="ob-btn" href={`/setup/morning${toParams({ ...c, step: 1 })}`}><span aria-hidden="true">←</span> {t('Back')}</Link>
        <span className="grow" />
        <Link className="ob-btn" href="/system/integrations">{t('Finish later')}</Link>
        <button type="button" className="ob-btn primary" disabled={!found} onClick={test} title={found ? undefined : t('Available once OpsTide runs with this destination')}>{t('Send a test message')}</button>
      </nav>
    </>
  )
}
