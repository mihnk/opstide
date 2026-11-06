import { query, type System as SystemData } from '../api'
import { Failed, Loading } from '../components/Shell'
import { Timeline } from '../components/Timeline'
import { useApi } from '../live'
import { ago, duration, hhmm, hhmmss, relativeTime } from '../time'

const kindName: Record<string, string> = { cronjobs: 'CronJobs', jobs: 'Jobs', pods: 'Pods', deployments: 'Deployments', replicasets: 'ReplicaSets', deploypods: 'Deployment Pods' }

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
  const scope = data.namespaces.length ? `${data.namespaces.length} namespace${data.namespaces.length === 1 ? '' : 's'} (${data.namespaces.join(', ')})` : 'the whole cluster'

  return (
    <div className="page">
      <div className="head-row">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">What OpsTide can see</span>
          <h1 className="title">Collector and coverage</h1>
        </div>
        <span className="grow" />
        <span className="mono muted" style={{ fontSize: 12, paddingBottom: 6 }}>opstide {data.version} · {data.cluster} · read-only</span>
      </div>

      <p className="sentence">
        {data.synced ? <>Watching {data.scopes.length ? <b>{new Set(data.scopes.map((s) => s.kind)).size} kinds</b> : 'nothing yet'} in <b>{scope}</b> since <span className="mono">{relativeTime(data.startedAt, data.now, tz)}</span>.</> : <>The watches are still syncing.</>}{' '}
        {gapCount > 0 ? <>Today Jobs were not watched {gapCount === 1 ? 'once' : `${gapCount} times`}; entries from those gaps may be missing.</> : 'No gaps today.'}{' '}
        {problems.length > 0 ? <>{problems.length === 1 ? 'One watch is' : `${problems.length} watches are`} failing: <b>{problems.map((p) => p.scope).join(', ')}</b>.</> : ''}
      </p>

      <section aria-label="Coverage today" style={{ display: 'flex', flexDirection: 'column' }}>
        <Timeline label="Watch · today" from={data.from} to={to} tz={tz} now={data.now} gaps={[]}
          lanes={data.scopes.map((s) => ({
            key: s.scope,
            name: `${kindName[s.kind] ?? s.kind}${s.namespace ? ` · ${s.namespace}` : ''}`,
            meta: s.problem ? (/forbidden/i.test(s.problem) ? 'not allowed · see Watches' : 'failing · see Watches') : s.watching.length ? `${s.watching.length === 1 ? 'continuous' : `${s.watching.length - 1} gap${s.watching.length > 2 ? 's' : ''}`}` : 'not watching',
            marks: s.watching.map((w) => ({ start: w.from, end: w.to, state: 'succeeded' as const })),
            markTitle: (m) => `watching ${hhmm(m.start, tz)}–${hhmm(m.end!, tz)}`,
          }))} />
        <div className="legend">
          <span><i style={{ height: 6, background: 'var(--run)' }} />watching, in sync</span>
          <span>Between bars OpsTide was not watching; entries from then may be missing. After a gap it relists and records what it finds, never the steps it did not see.</span>
        </div>
      </section>

      <div className="two">
        <section aria-label="Collector">
          <h2 className="label section-title">Collector</h2>
          <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
            <dt className="label">Process</dt><dd>up {duration((nowMs - Date.parse(data.startedAt)) / 1000)} <span className="muted">· 1 replica, 1 writer</span></dd>
            <dt className="label">Last write</dt><dd className="mono">{data.lastWrite ? <>{hhmmss(data.lastWrite, tz)} <span className="muted">{ago(data.lastWrite, nowMs)}</span></> : '—'}</dd>
            <dt className="label">Ready</dt><dd>{data.synced ? <><span className="mono">/readyz</span> ok <span className="muted">· all watches synced, storage writable</span></> : <span className="fail">watches not synced yet</span>}</dd>
            <dt className="label">Scope</dt><dd>{data.namespaces.length ? data.namespaces.join(', ') : 'all namespaces'}</dd>
          </dl>
        </section>
        <section aria-label="Storage">
          <h2 className="label section-title">Storage</h2>
          <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
            <dt className="label">Database</dt><dd>SQLite <span className="muted">· schema v{data.storage.schemaVersion}</span></dd>
            <dt className="label">Size</dt><dd className="mono">{bytes(data.storage.bytes)} <span className="muted">+ WAL {bytes(data.storage.walBytes)}</span></dd>
            <dt className="label">Entries</dt><dd><span className="mono">{data.storage.entries.toLocaleString('en')}</span> <span className="muted">of at most {data.storage.maxEntries.toLocaleString('en')}{data.storage.oldestEntry ? ` · oldest ${new Date(data.storage.oldestEntry).toLocaleDateString('en-GB', { timeZone: tz, day: 'numeric', month: 'short' })}` : ''}</span></dd>
            <dt className="label">Keeps</dt><dd>{data.storage.retentionDays} days or {data.storage.maxEntries.toLocaleString('en')} entries, whichever comes first</dd>
            <dt className="label">Last prune</dt><dd className="mono">{data.storage.lastPrune ? <>{hhmm(data.storage.lastPrune, tz)} <span className="muted">· {data.storage.lastPruneRemoved.toLocaleString('en')} removed</span></> : '—'}</dd>
            <dt className="label">Checkpoint</dt><dd className="mono">{data.storage.lastCheckpoint ? hhmm(data.storage.lastCheckpoint, tz) : <span className="muted">every 5 min</span>}</dd>
            <dt className="label">Backup</dt><dd className="muted">Not configured</dd>
          </dl>
        </section>
      </div>

      <section aria-label="Release observation" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <h2 className="label section-title">Release observation <span className="note">· Deployments, their ReplicaSets and Pods</span></h2>
        <dl className="facts" style={{ gridTemplateColumns: '170px minmax(0, 1fr)' }}>
          <dt className="label">Observation window</dt><dd>{data.workload.windowMinutes} min after a rollout completes</dd>
          <dt className="label">Under observation</dt><dd className="mono">{data.workload.activeWindows} release{data.workload.activeWindows === 1 ? '' : 's'}</dd>
          <dt className="label">Last release entry</dt><dd className="mono">{data.workload.lastEvent ? <>{hhmmss(data.workload.lastEvent, tz)} <span className="muted">{ago(data.workload.lastEvent, nowMs)}</span></> : <span className="muted" style={{ fontFamily: 'inherit' }}>none yet</span>}</dd>
          <dt className="label">Completeness</dt>
          <dd>{data.workload.degraded.length === 0
            ? <>Complete: Deployments, ReplicaSets and their Pods are being watched.</>
            : <ul style={{ margin: 0, paddingLeft: 18 }}>{data.workload.degraded.map((m, i) => <li key={i} className="fail">{m}</li>)}</ul>}</dd>
        </dl>
      </section>

      <div className="two">
        <section aria-label="Watches" className="rows">
          <h2 className="label section-title">Watches <span className="note">· get, list and watch only</span></h2>
          {data.scopes.length === 0 && <div className="empty">No watch has started yet.</div>}
          {data.scopes.map((s) => (
            <div key={s.scope} className="row" style={{ gridTemplateColumns: '22px minmax(0, 1fr) auto' }}>
              <span className="mono" aria-hidden="true" style={{ color: s.problem ? 'var(--fail)' : 'var(--muted)' }}>{s.problem ? '✕' : '✓'}</span>
              <span>{kindName[s.kind] ?? s.kind}{s.namespace ? ` in ${s.namespace}` : ''}{s.problem && <span className="sub" style={{ fontSize: 12 }}>{s.problem}</span>}</span>
              <span className="mono" style={{ fontSize: 12, color: s.problem ? 'var(--fail)' : 'var(--muted)' }}>{s.problem ? 'failing' : 'watching'}</span>
            </div>
          ))}
          <div className="row" style={{ gridTemplateColumns: '22px minmax(0, 1fr) auto' }}>
            <span className="mono muted" aria-hidden="true">–</span>
            <span>Secrets, logs, exec<span className="sub" style={{ fontSize: 12 }}>not requested and not needed</span></span>
            <span className="mono muted" style={{ fontSize: 12 }}>not asked</span>
          </div>
        </section>
        <section aria-label="Integrations" className="rows">
          <h2 className="label section-title">Integrations <span className="note">· nothing leaves the cluster by default</span></h2>
          {data.integrations.map((g) => (
            <div key={g.name} className="row" style={{ gridTemplateColumns: 'minmax(0, 1fr) auto' }}>
              <span>{g.name}<span className="sub" style={{ fontSize: 12 }}>{g.note}</span></span>
              <span className="mono" style={{ fontSize: 12, color: g.state === 'ready' ? 'var(--ink)' : 'var(--muted)' }}>{g.state}</span>
            </div>
          ))}
        </section>
      </div>
    </div>
  )
}
