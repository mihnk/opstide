import { query, type System as SystemData } from '../api'
import { Failed, Loading } from '../components/Shell'
import { Timeline } from '../components/Timeline'
import { useApi } from '../live'
import { ago, duration, hhmm, hhmmss, relativeTime } from '../time'
import { kindName } from '../kinds'
import { locale, t, tn } from '../i18n'
import { Link } from '../router'
import { tParts } from '../components/status'


function bytes(n: number): string {
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`
  if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(n < 10 * 1024 * 1024 ? 1 : 0)} MB`
  return `${(n / 1024 ** 3).toFixed(1)} GB`
}

export function System({ tz }: { tz: string }) {
  const { data, error } = useApi<SystemData>(`/api/v1/system${query({ tz })}`)
  if (error && !data) return <div className="page"><Failed error={error} /></div>
  if (!data) return <div className="page"><Loading /></div>

  const nowMs = Date.parse(data.now)
  const to = new Date(Date.parse(data.from) + 24 * 3600e3).toISOString()
  const problems = data.scopes.filter((s) => s.problem)
  const gapsToday = data.scopes.find((s) => s.kind === 'jobs')?.watching ?? []
  const gapCount = Math.max(0, gapsToday.length - 1)
  const scope = data.namespaces.length ? tn(data.namespaces.length, '{n} namespace ({list})', '{n} namespaces ({list})', { list: data.namespaces.join(', ') }) : t('the whole cluster')

  return (
    <div className="page">
      <div className="head-row">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">{t('What OpsTide can see')}</span>
          <h1 className="title">{t('Collector and coverage')}</h1>
        </div>
        <span className="grow" />
        <span className="mono muted" style={{ fontSize: 12, paddingBottom: 6 }}>opstide {data.version} · {data.cluster} · {t('read-only')} · <Link href="/welcome">{t('Guide')}</Link></span>
      </div>

      {data.dropped.count > 0 && (
        <div className="banner" role="note">
          <span className="label">{t('Gave up')}</span>
          <span>{tn(data.dropped.count, 'OpsTide gave up recording {n} object after repeated failures', 'OpsTide gave up recording {n} objects after repeated failures')}{data.dropped.last ? <>, {t('last at')} <span className="mono">{hhmmss(data.dropped.last, tz)}</span></> : null}. {t("Each is a gap in coverage over the stretch its changes may have been lost; the Log names the object. Look at OpsTide's own log for the cause.")}</span>
        </div>
      )}

      <p className="sentence">
        {data.synced ? (data.scopes.length
          ? tParts('Watching {kinds} in {scope} since {since}.', { kinds: <b>{tn(new Set(data.scopes.map((s) => s.kind)).size, '{n} kinds', '{n} kinds')}</b>, scope: <b>{scope}</b>, since: <span className="mono">{relativeTime(data.startedAt, data.now, tz)}</span> })
          : tParts('Watching nothing yet in {scope} since {since}.', { scope: <b>{scope}</b>, since: <span className="mono">{relativeTime(data.startedAt, data.now, tz)}</span> })) : t('The watches are still syncing.')}{' '}
        {gapCount > 0 ? tn(gapCount, 'Today Jobs were not watched once; entries from those gaps may be missing.', 'Today Jobs were not watched {n} times; entries from those gaps may be missing.') : t('No gaps today.')}{' '}
        {problems.length > 0 ? <>{tn(problems.length, 'One watch is failing:', '{n} watches are failing:')} <b>{problems.map((p) => p.scope).join(', ')}</b>.</> : ''}
      </p>

      <section aria-label={t('Coverage today')} style={{ display: 'flex', flexDirection: 'column' }}>
        <Timeline label={t('Watch · today')} from={data.from} to={to} tz={tz} now={data.now} gaps={[]}
          lanes={data.scopes.map((s) => ({
            key: s.scope,
            name: `${t(kindName[s.kind] ?? s.kind)}${s.namespace ? ` · ${s.namespace}` : ''}`,
            meta: s.problem ? (/forbidden/i.test(s.problem) ? t('not allowed · see Watches') : t('failing · see Watches')) : s.watching.length ? (s.watching.length === 1 ? t('continuous') : tn(s.watching.length - 1, '{n} gap', '{n} gaps')) : t('not watching'),
            marks: s.watching.map((w) => ({ start: w.from, end: w.to, state: 'succeeded' as const })),
            markTitle: (m) => t('watching {from}–{to}', { from: hhmm(m.start, tz), to: hhmm(m.end!, tz) }),
          }))} />
        <div className="legend">
          <span><i style={{ height: 6, background: 'var(--run)' }} />{t('watching, in sync')}</span>
          <span>{t('Between bars OpsTide was not watching; entries from then may be missing. After a gap it relists and records what it finds, never the steps it did not see.')}</span>
        </div>
      </section>

      <div className="two">
        <section aria-label={t('Collector')}>
          <h2 className="label section-title">{t('Collector')}</h2>
          <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
            <dt className="label">{t('Process')}</dt><dd>{t('up {d}', { d: duration((nowMs - Date.parse(data.startedAt)) / 1000) })} <span className="muted">· {t('1 replica, 1 writer')}</span></dd>
            <dt className="label">{t('Last write')}</dt><dd className="mono">{data.lastWrite ? <>{hhmmss(data.lastWrite, tz)} <span className="muted">{ago(data.lastWrite, nowMs)}</span></> : '—'}</dd>
            <dt className="label">{t('Ready')}</dt><dd>{data.synced ? <><span className="mono">/readyz</span> ok <span className="muted">· {t('all watches synced, storage writable')}</span></> : <span className="fail">{t('watches not synced yet')}</span>}</dd>
            <dt className="label">{t('Scope')}</dt><dd>{data.namespaces.length ? data.namespaces.join(', ') : t('all namespaces')}</dd>
          </dl>
        </section>
        <section aria-label={t('Storage')}>
          <h2 className="label section-title">{t('Storage')}</h2>
          <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
            <dt className="label">{t('Database')}</dt><dd>SQLite <span className="muted">· schema v{data.storage.schemaVersion}</span></dd>
            <dt className="label">{t('Size')}</dt><dd className="mono">{bytes(data.storage.bytes)} <span className="muted">+ WAL {bytes(data.storage.walBytes)}</span></dd>
            <dt className="label">{t('Entries')}</dt><dd><span className="mono">{data.storage.entries.toLocaleString('en')}</span> <span className="muted">{t('of at most {n}', { n: data.storage.maxEntries.toLocaleString('en') })}{data.storage.oldestEntry ? ` · ${t('oldest {date}', { date: new Date(data.storage.oldestEntry).toLocaleDateString(locale(), { timeZone: tz, day: 'numeric', month: 'short' }) })}` : ''}</span></dd>
            <dt className="label">{t('Keeps')}</dt><dd>{t('{d} days or {n} entries, whichever comes first', { d: data.storage.retentionDays, n: data.storage.maxEntries.toLocaleString('en') })}</dd>
            <dt className="label">{t('Held since')}</dt>
            <dd>{data.storage.sources.map((x) => (
              <span key={x.name} style={{ display: 'block' }}>{t(x.label)}: {x.retainedSince ? <span className="mono">{new Date(x.retainedSince).toLocaleDateString(locale(), { timeZone: tz, day: 'numeric', month: 'short' })} {hhmm(x.retainedSince, tz)}</span> : <span className="muted">{t('none held')}</span>}
                {x.cappedTotal > 0 && <span className="fail"> · {t('{n} removed by the size budget, not age', { n: x.cappedTotal.toLocaleString('en') })}</span>}</span>
            ))}</dd>
            <dt className="label">{t('Last prune')}</dt><dd className="mono">{data.storage.lastPrune ? <>{hhmm(data.storage.lastPrune, tz)} <span className="muted">· {t('{n} removed', { n: data.storage.lastPruneRemoved.toLocaleString('en') })}</span></> : '—'}</dd>
            <dt className="label">{t('Checkpoint')}</dt><dd className="mono">{data.storage.lastCheckpoint ? hhmm(data.storage.lastCheckpoint, tz) : <span className="muted">{t('every 5 min')}</span>}</dd>
            <dt className="label">{t('Backup')}</dt><dd className="muted">{t('Not configured')}</dd>
          </dl>
        </section>
      </div>

      <section aria-label={t('Release observation')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">{t('Release observation')} <span className="note">· {t('Deployments, their ReplicaSets and Pods')}</span></h2>
        <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
          <dt className="label">{t('Observation window')}</dt><dd>{t('{n} min after a rollout completes', { n: data.workload.windowMinutes })}</dd>
          <dt className="label">{t('Under observation')}</dt><dd className="mono">{tn(data.workload.activeWindows, '{n} release', '{n} releases')}</dd>
          <dt className="label">{t('Last release entry')}</dt><dd className="mono">{data.workload.lastEvent ? <>{hhmmss(data.workload.lastEvent, tz)} <span className="muted">{ago(data.workload.lastEvent, nowMs)}</span></> : <span className="muted" style={{ fontFamily: 'inherit' }}>{t('none yet')}</span>}</dd>
          <dt className="label">{t('Completeness')}</dt>
          <dd>{data.workload.degraded.length === 0
            ? <>{t('Complete: Deployments, ReplicaSets and their Pods are being watched.')}</>
            : <ul style={{ margin: 0, paddingLeft: 18 }}>{data.workload.degraded.map((m, i) => <li key={i} className="fail">{m}</li>)}</ul>}</dd>
        </dl>
      </section>

      <section aria-label={t('Workloads and Nodes')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">{t('Workloads and Nodes')} <span className="note">· StatefulSets, DaemonSets, Nodes</span></h2>
        <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
          <dt className="label">{t('Workloads recorded')}</dt>
          <dd className="mono">{['Deployment', 'StatefulSet', 'DaemonSet'].map((k) => `${data.workloads.counts[k] ?? 0} ${k}${(data.workloads.counts[k] ?? 0) === 1 ? '' : 's'}`).join(' · ')}</dd>
          <dt className="label">{t('Node observation')}</dt>
          <dd>{data.workloads.nodes.enabled ? <><span className="mono">{t('on')}</span> <span className="muted">· {tn(data.workloads.nodes.live, '{n} live Node recorded', '{n} live Nodes recorded')}</span></> : <><span className="mono">{t('off')}</span> <span className="muted">· {tn(data.workloads.nodes.recorded, '{n} Node recorded earlier, none shown as live', '{n} Nodes recorded earlier, none shown as live')}{data.workloads.nodes.lastObservedAt ? <> · {t('last observed')} <span className="mono">{hhmmss(data.workloads.nodes.lastObservedAt, tz)}</span></> : ` · ${t('never observed')}`}</span></>}<span className="muted" style={{ display: 'block', fontSize: 12 }}>{data.workloads.nodes.note}</span></dd>
          <dt className="label">{t('Completeness')}</dt>
          <dd>{data.workloads.degraded.length === 0
            ? <>{t('Complete as far as OpsTide can tell for the kinds it is set to watch.')}</>
            : <ul style={{ margin: 0, paddingLeft: 18 }}>{data.workloads.degraded.map((m, i) => <li key={i} className="fail">{m}</li>)}</ul>}</dd>
        </dl>
      </section>

      <section aria-label={t('ConfigMaps and Secrets')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">{t('ConfigMaps and Secrets')} <span className="note">· {t('who created, wrote or deleted them; never a value')}</span></h2>
        <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
          <dt className="label">ConfigMaps</dt>
          <dd>{data.config.configMaps ? <><span className="mono">{t('on')}</span> <span className="muted">· {tn(data.config.configMapCount, '{n} ConfigMap recorded', '{n} ConfigMaps recorded')}</span></> : <span className="mono">{t('off')}</span>}<span className="muted" style={{ display: 'block', fontSize: 12 }}>{data.config.configMapNote}</span></dd>
          <dt className="label">{t('Secret metadata')}</dt>
          <dd>{data.config.secretMetadata ? <><span className="mono">{t('on')}</span> <span className="muted">· {tn(data.config.secretCount, '{n} Secret recorded', '{n} Secrets recorded')}</span></> : <span className="mono">{t('off')}</span>}<span className="muted" style={{ display: 'block', fontSize: 12 }}>{data.config.secretNote}</span></dd>
          <dt className="label">{t('Completeness')}</dt>
          <dd>{data.config.degraded.length === 0
            ? <>{t('Complete as far as OpsTide can tell for the kinds it is set to watch.')}</>
            : <ul style={{ margin: 0, paddingLeft: 18 }}>{data.config.degraded.map((m, i) => <li key={i} className="fail">{m}</li>)}</ul>}</dd>
        </dl>
      </section>

      <section aria-label={t('Maintenance windows')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">{t('Maintenance windows')} <span className="note">· {t('declared in the configuration (Helm values), not here')}</span></h2>
        {(data.maintenance?.windows.length ?? 0) === 0 ? (
          <div className="empty">{t('No maintenance window is declared. Set maintenance.windows in the Helm values to mark planned work.')}</div>
        ) : (
          <div className="table-wrap">
            <table className="data compact" style={{ minWidth: 760 }}>
              <thead><tr><th className="label">{t('Window')}</th><th className="label">{t('When')}</th><th className="label">{t('Namespaces')}</th><th className="label">{t('Now and next')}</th></tr></thead>
              <tbody>
                {data.maintenance!.windows.map((w) => (
                  <tr key={w.name}>
                    <td><span style={{ fontWeight: 500 }}>{w.name}</span>{w.active && <span className="chip maint">{t('open now')}</span>}{w.note && <span className="muted" style={{ display: 'block', fontSize: 12 }}>{w.note}</span>}</td>
                    <td className="mono" style={{ fontSize: 13 }}>{w.kind === 'recurring'
                      ? <>{w.cron} <span className="muted">· {w.duration} · {w.timeZone}</span></>
                      : <>{w.start && hhmmss(w.start, tz)} → {w.end && hhmmss(w.end, tz)} <span className="muted" style={{ fontFamily: 'inherit' }}>· {t('once')}</span></>}</td>
                    <td>{w.namespaces.length ? <span className="mono">{w.namespaces.join(', ')}</span> : <span className="muted">{t('the whole cluster')}</span>}
                      {(w.unwatchedNamespaces?.length ?? 0) > 0 && <span className="fail" style={{ display: 'block', fontSize: 12 }}>{t('Not watched by OpsTide: {list}. Nothing is recorded there, so this window marks nothing in them.', { list: w.unwatchedNamespaces!.join(', ') })}</span>}</td>
                    <td className="mono" style={{ fontSize: 12 }}>{w.next.length === 0 ? <span className="muted" style={{ fontFamily: 'inherit' }}>{t('none to come')}</span>
                      : w.next.map((o) => <span key={o.from} style={{ display: 'block' }}>{new Date(o.from).toLocaleString(locale(), { timeZone: tz, weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })} – {hhmm(o.to, tz)}</span>)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {data.maintenance && <span className="muted" style={{ fontSize: 12 }}>{t('A record inside a window is marked with it, never hidden: it is still listed and counted. opstide_maintenance_active says which window is open, for alert rules.')}</span>}
      </section>

      <section aria-label={t('Evidence')} style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">{t('Evidence')} <span className="note">· {t('Kubernetes Events and autoscalers')}</span></h2>
        <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
          <dt className="label">{t('Events read through')}</dt><dd className="mono">{data.evidence.source || <span className="muted" style={{ fontFamily: 'inherit' }}>{t('not chosen yet')}</span>}</dd>
          <dt className="label">{t('Events kept')}</dt><dd><span className="mono">{data.evidence.records.toLocaleString('en')}</span> <span className="muted">· {data.evidence.warnings.toLocaleString('en')} Warning · {t('kept after Kubernetes drops them, for the retention period')}</span></dd>
          <dt className="label">{t('Last new Event')}</dt><dd className="mono">{data.evidence.latestObserved ? <>{hhmmss(data.evidence.latestObserved, tz)} <span className="muted">{ago(data.evidence.latestObserved, nowMs)}</span></> : <span className="muted" style={{ fontFamily: 'inherit' }}>{t('none yet')}</span>}</dd>
          <dt className="label">{t('Autoscalers')}</dt><dd><span className="mono">{data.evidence.autoscalers}</span> <span className="muted">{data.evidence.autoscalers === 1 ? t('HorizontalPodAutoscaler observed') : t('HorizontalPodAutoscalers observed')}</span></dd>
          <dt className="label">{t('Completeness')}</dt>
          <dd>{data.evidence.degraded.length === 0
            ? <>{t('Complete as far as OpsTide can tell: Events and autoscalers are being read.')}</>
            : <ul style={{ margin: 0, paddingLeft: 18 }}>{data.evidence.degraded.map((m, i) => <li key={i} className="fail">{m}</li>)}</ul>}</dd>
        </dl>
      </section>

      <div className="two">
        <section aria-label={t('Watches')} className="rows">
          <h2 className="label section-title">{t('Watches')} <span className="note">· {t('get, list and watch only')}</span></h2>
          {data.scopes.length === 0 && <div className="empty">{t('No watch has started yet.')}</div>}
          {data.scopes.map((s) => (
            <div key={s.scope} className="row" style={{ gridTemplateColumns: '22px minmax(0, 1fr) auto' }}>
              <span className="mono" aria-hidden="true" style={{ color: s.problem ? 'var(--fail)' : 'var(--muted)' }}>{s.problem ? '✕' : '✓'}</span>
              <span>{t(kindName[s.kind] ?? s.kind)}{s.namespace ? ` ${t('in {ns}', { ns: s.namespace })}` : ''}{s.problem && <span className="sub" style={{ fontSize: 12 }}>{s.problem}</span>}</span>
              <span className="mono" style={{ fontSize: 12, color: s.problem ? 'var(--fail)' : 'var(--muted)' }}>{s.problem ? t('failing') : t('watching')}</span>
            </div>
          ))}
          <div className="row" style={{ gridTemplateColumns: '22px minmax(0, 1fr) auto' }}>
            <span className="mono muted" aria-hidden="true">–</span>
            <span>{data.config.secretMetadata ? t('Secret data, logs, exec') : t('Secrets, logs, exec')}<span className="sub" style={{ fontSize: 12 }}>{data.config.secretMetadata ? t('Secrets are read as metadata only; their data is never requested') : t('not requested and not needed')}</span></span>
            <span className="mono muted" style={{ fontSize: 12 }}>{t('not asked')}</span>
          </div>
        </section>
        <section aria-label={t('Integrations')} className="rows">
          <h2 className="label section-title">{t('Integrations')} <span className="note">· {t('nothing leaves the cluster by default')}</span></h2>
          {data.integrations.map((g) => (
            <div key={g.name} className="row" style={{ gridTemplateColumns: 'minmax(0, 1fr) auto' }}>
              <span>{g.name}<span className="sub" style={{ fontSize: 12 }}>{g.note}</span></span>
              <span className="mono" style={{ fontSize: 12, color: g.state === 'ready' ? 'var(--ink)' : 'var(--muted)' }}>{t(g.state)}</span>
            </div>
          ))}
        </section>
      </div>
    </div>
  )
}
