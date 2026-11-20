import { useState } from 'react'
import { postJSON, query, type Delivery, type MorningPreview, type NotifyStatus } from '../api'
import { Failed, Loading } from '../components/Shell'
import { coversWords, destinationIcon, includeWords, MorningMessage, notifyValues, sourceParts, typeWords } from '../components/Morning'
import { SystemTabs } from './System'
import { t } from '../i18n'
import { useApi } from '../live'
import { Link } from '../router'
import { relativeTime } from '../time'

/** System › Integrations: where the morning summary goes, how its deliveries went, and what it says, from the chart's
 *  configuration. Nothing here changes it: that is the chart's, so the token cannot send the record elsewhere. */
export function Integrations({ tz }: { tz: string }) {
  const { data: st, error } = useApi<NotifyStatus>('/api/v1/notify', { refreshMs: 30_000 })
  const { data: preview } = useApi<MorningPreview>(st ? `/api/v1/morning${query({ tz: st.configured ? undefined : tz })}` : null)
  if (error && !st) return <div className="page"><Failed error={error} /></div>
  if (!st) return <div className="page"><Loading /></div>
  const now = new Date().toISOString()
  const configured = new Set(st.destinations.map((d) => d.type))
  const channel = st.destinations[0]?.name ?? '#channel'

  return (
    <div className="page">
      <div className="head-row">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">{t('What OpsTide reports, and where')}</span>
          <h1 className="title">{t('Integrations')}</h1>
        </div>
        <span className="grow" />
        <Link className="button" style={{ display: 'inline-flex', alignItems: 'center' }} href="/setup/morning">{t('Set up a destination')}</Link>
      </div>
      <SystemTabs current="integrations" />
      <p className="sentence">
        {st.configured
          ? t('OpsTide sends one thing out of the cluster: a morning summary, to the places set up here. Nothing else leaves it.')
          : t('OpsTide sends nothing out of the cluster. It can send a morning summary to Slack, Microsoft Teams or a webhook of yours; nothing else would leave it.')}
      </p>
      {st.demo && <div className="banner" role="note"><span className="label">{t('Demo')}</span><span>{t('The destination below is made up, and nothing is sent.')}</span></div>}

      <div className="int-grid">
        <div className="int-main">
          <section className="int-sec" aria-label={t('Morning summary')}>
            <h2 className="int-h">{t('Morning summary')} <span className="note">{t('what the night looked like, before the day starts')}</span></h2>
            {st.configured ? (
              <>
                <div className="int-status">
                  <span className={`int-dot${st.destinations.every((d) => !d.last || d.last.ok) ? '' : ' bad'}`} aria-hidden="true" />
                  <div className="int-status-text">
                    <b>{t('{schedule} in {zone}', { schedule: st.schedule ?? '', zone: st.timeZone ?? '' })}</b>
                    <span className="muted fine">
                      {st.lastSent ? t('Last sent {when}', { when: relativeTime(st.lastSent, now, tz) }) : t('Not sent yet')}
                      {st.next ? ` · ${t('next {when}', { when: relativeTime(st.next, now, tz) })}` : ''}
                    </span>
                  </div>
                </div>
                <dl className="facts">
                  <dt className="label">{t('Covers')}</dt><dd>{t(coversWords[st.covers ?? ''] ?? st.covers ?? '')}</dd>
                  <dt className="label">{t('Says')}</dt><dd>{(st.include ?? []).map((k) => t(includeWords.find(([w]) => w === k)?.[1] ?? k)).join('; ')}</dd>
                  <dt className="label">{t('Language')}</dt><dd>{st.language === 'tr' ? 'Türkçe' : 'English'}</dd>
                  <dt className="label">{t('When quiet')}</dt><dd>{t('it says "Nothing needs a look", so silence is never ambiguous')}</dd>
                </dl>
              </>
            ) : (
              <div className="dashed">
                {t('Not set up. Setup asks where and when, shows the message your channel would get, and writes the chart values and the Secret command for you.')}{' '}
                <Link href="/setup/morning">{t('Set it up')}</Link>
              </div>
            )}
          </section>

          <section className="int-sec" aria-label={t('Destinations')}>
            <h2 className="int-h">{t('Destinations')} <span className="note">{t('each URL is kept in a Secret; OpsTide never shows it')}</span></h2>
            <div>
              {st.destinations.map((d) => <DestinationRow key={d.name} d={d} demo={!!st.demo} now={now} tz={tz} />)}
              {(['slack', 'teams', 'webhook'] as const).filter((k) => !configured.has(k)).map((k) => {
                const Icon = destinationIcon[k]
                return (
                  <div key={k} className="int-dest">
                    <span className="int-ico"><Icon /></span>
                    <span className="int-dest-name"><b>{t(typeWords[k][0])}</b><span className="muted fine">{t(typeWords[k][1])}</span></span>
                    <span className="muted">{t('Not set up')}</span>
                    <span />
                    <Link className="button small" href={`/setup/morning${query({ type: k })}`}>{t('Set up')}</Link>
                  </div>
                )
              })}
            </div>
          </section>

          {st.configured && (
            <section className="int-sec" aria-label={t('Where this is set')}>
              <h2 className="int-h">{t('Where this is set')} <span className="note">{t('in the chart, reviewed like any other change')}</span></h2>
              <pre className="code-block">{notifyValues({
                schedule: st.schedule ?? '', timeZone: st.timeZone ?? 'UTC', covers: st.covers ?? 'since-18h', language: st.language ?? 'en', include: st.include ?? [],
                destinations: st.destinations.map((d) => ({ type: d.type, name: d.name, ...(sourceParts(d.source) ?? { secret: '…', key: '…' }) })),
              })}</pre>
              <p className="muted fine" style={{ margin: 0 }}>{t('Changing it from this page would let anyone with the token send the record somewhere else, so the page only reads it. Setup writes the values for you.')}</p>
            </section>
          )}
        </div>

        <aside className="int-aside">
          <span className="label">{st.configured ? t('Preview · built from the record now') : t('Preview · what a channel would get now')}</span>
          {preview ? <MorningMessage lines={preview.lines} channel={channel} when={relativeTime(now, now, tz)} /> : <Loading />}
          <span className="muted fine">{t('Every line links to its record when publicURL is set. Each destination gets the same summary, in its own format.')}</span>
        </aside>
      </div>
    </div>
  )
}

function DestinationRow({ d, demo, now, tz }: { d: NotifyStatus['destinations'][number]; demo: boolean; now: string; tz: string }) {
  const [busy, setBusy] = useState(false)
  const [res, setRes] = useState<Delivery | string | null>(null)
  const Icon = destinationIcon[d.type]
  const test = async () => {
    setBusy(true)
    setRes(null)
    try {
      setRes(await postJSON<Delivery>('/api/v1/notify/test', { destination: d.name }))
    } catch (e) {
      setRes(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const last = d.last
  return (
    <div className="int-dest">
      <span className="int-ico"><Icon /></span>
      <span className="int-dest-name"><b>{t(typeWords[d.type][0])}</b><span className="muted fine">{t(typeWords[d.type][1])}</span></span>
      <span className="int-dest-name"><span className="mono">{d.name}</span><span className="muted fine">{d.source}</span></span>
      <span className="int-dest-name" role="status">
        {res && typeof res !== 'string' ? (
          <><span className={res.ok ? 'ok' : 'fail'}>{res.ok ? t('● Test delivered') : t('Test not delivered')}</span><span className="muted fine">{res.error ?? (res.status ? `HTTP ${res.status}` : '')}</span></>
        ) : typeof res === 'string' ? (
          <span className="fail fine">{res}</span>
        ) : last ? (
          <><span className={last.ok ? 'ok' : 'fail'}>{last.ok ? t('● Delivered') : t('Not delivered')}</span><span className="muted mono fine">{relativeTime(last.at, now, tz)}{last.status ? ` · ${last.status}` : ''}{last.error ? ` · ${last.error}` : ''}</span></>
        ) : (
          <span className="muted">{t('Nothing sent yet')}</span>
        )}
      </span>
      <button type="button" className="button small" disabled={busy} onClick={test} title={demo ? t('The demo sends nothing') : t('Sends the summary as it would be now, marked as a test')}>
        {busy ? t('Sending…') : t('Send a test')}
      </button>
    </div>
  )
}
