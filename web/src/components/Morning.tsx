import type { DigestLine } from '../api'
import { t } from '../i18n'

/** The morning summary as a chat channel shows it: built from the same lines OpsTide sends, so the preview is the
 *  message. Neutral on purpose: it is not drawn as any one product. */
export function MorningMessage({ lines, channel, when }: { lines: DigestLine[]; channel: string; when: string }) {
  return (
    <div className="msg-frame" aria-label={t('Preview of the message')}>
      <div className="msg-top">
        <ChatIcon />
        <b>{channel}</b>
        <span className="grow" />
        <span className="mono">{when}</span>
      </div>
      <div className="msg">
        <div className="msg-avatar" aria-hidden="true">OT</div>
        <div className="msg-body">
          <div className="msg-who"><b>OpsTide</b><span className="msg-app">APP</span></div>
          {lines.map((l, i) => (
            <div key={i} className={`msg-line ${l.kind}`}>
              {l.parts.map((p, j) => {
                const cls = [p.bold ? 'b' : '', p.fail ? 'fail' : '', p.mono ? 'mono' : ''].filter(Boolean).join(' ') || undefined
                return p.link ? (
                  <a key={j} href={p.link} className={cls} target="_blank" rel="noopener noreferrer">{p.text}</a>
                ) : (
                  <span key={j} className={cls}>{p.text}</span>
                )
              })}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

export function ChatIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" aria-hidden="true">
      <path d="M2.5 3.5h11v7h-6l-3 2.5v-2.5h-2z" />
    </svg>
  )
}

export function TeamIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
      <circle cx="6" cy="5.5" r="2.2" /><path d="M2 13c.4-2.4 2-3.6 4-3.6s3.6 1.2 4 3.6" /><circle cx="11.5" cy="6.5" r="1.7" /><path d="M11 9.6c1.6 0 2.7.9 3 2.6" />
    </svg>
  )
}

export function HookIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M5.5 3.5 2.5 8l3 4.5M10.5 3.5l3 4.5-3 4.5" />
    </svg>
  )
}

export const destinationIcon = { slack: ChatIcon, teams: TeamIcon, webhook: HookIcon }

/** What a summary may include, as people say it; the keys are the API's. */
export const includeWords: [string, string][] = [
  ['failures', 'Failed runs and overdue CronJobs'],
  ['troubles', 'Restarts, OOMKills and failed releases, with what changed in the hour before'],
  ['gaps', 'Stretches OpsTide was not watching'],
  ['due', 'What is due today'],
  ['changes', 'Every change recorded, not only the count'],
]

export const coversWords: Record<string, string> = { 'since-18h': 'Since 18:00 the day before', '12h': 'Last 12 h', '24h': 'Last 24 h' }

export const typeWords: Record<string, [string, string]> = {
  slack: ['Slack', 'An incoming webhook for one channel'],
  teams: ['Microsoft Teams', 'A Workflows webhook for one channel'],
  webhook: ['Webhook', 'JSON to any URL you run'],
}

/** The notify block of the chart's values, as setup writes it and Integrations shows it. */
export function notifyValues(v: {
  schedule: string; timeZone: string; covers: string; language: string; include: string[]
  destinations: { type: string; name: string; secret: string; key: string }[]
}): string {
  const q = (s: string) => JSON.stringify(s)
  return [
    'notify:',
    '  morningSummary:',
    `    schedule: ${q(v.schedule)}`,
    `    timeZone: ${v.timeZone}`,
    `    covers: ${v.covers}`,
    `    language: ${v.language}`,
    `    include: [${v.include.join(', ')}]`,
    '  destinations:',
    ...v.destinations.flatMap((d) => [`    - type: ${d.type}`, `      name: ${q(d.name)}`, `      urlFrom: { secret: ${d.secret}, key: ${d.key} }`]),
  ].join('\n')
}

/** "Secret opstide-notify, key slack" back into its parts. */
export function sourceParts(source: string): { secret: string; key: string } | null {
  const m = /^Secret (\S+), key (\S+)$/.exec(source)
  return m ? { secret: m[1], key: m[2] } : null
}
