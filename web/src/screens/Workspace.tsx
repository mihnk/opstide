import { useEffect, useMemo, useState } from 'react'
import { downloadPost, getJSON, postJSON, query } from '../api'
import { Failed } from '../components/Shell'
import { explorerLink, fill, groupWords, provWord, typeWord, type ExplorerContext, type ExplorerLookup, type ExplorerRecord } from '../explorer'
import { t, tn } from '../i18n'
import { Link } from '../router'
import { hhmmss, isoDate } from '../time'
import { clearWorkspace, removeFromWorkspace, useWorkspace, WORKSPACE_MAX } from '../workspace'
import { ZoneSelect } from './common'
import { Note } from '../components/Note'

// The Investigation Workspace: records a person picked, kept in this tab only. It counts what is there and says where
// coverage was not whole. It writes no conclusion: no summary, no ranking, no suggestion of cause.

function tally(rs: ExplorerRecord[], by: (r: ExplorerRecord) => string): [string, number][] {
  const m = new Map<string, number>()
  for (const r of rs) m.set(by(r), (m.get(by(r)) ?? 0) + 1)
  return [...m.entries()].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
}

function Counts({ title, rows }: { title: string; rows: [string, number][] }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      <h3 className="label">{title}</h3>
      <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 2 }}>
        {rows.map(([k, n]) => <li key={k} style={{ display: 'flex', gap: 10 }}><span className="mono" style={{ minWidth: 28, textAlign: 'right' }}>{n}</span><span>{k}</span></li>)}
      </ul>
    </div>
  )
}

export function Workspace({ tz, setTz }: { tz: string; setTz: (z: string) => void }) {
  const ids = useWorkspace()
  const [look, setLook] = useState<ExplorerLookup>()
  const [error, setError] = useState<Error>()
  const [asc, setAsc] = useState(true)
  const [confirmClear, setConfirmClear] = useState(false)
  const [msgs, setMsgs] = useState(false)
  const [busy, setBusy] = useState(false)
  const [ctx, setCtx] = useState<ExplorerContext>()

  const key = ids.join(' ')
  useEffect(() => {
    if (ids.length === 0) return
    const ctl = new AbortController()
    postJSON<ExplorerLookup>('/api/v1/explorer/lookup', { ids }, ctl.signal)
      .then((l) => {
        setLook(l)
        setError(undefined)
      })
      .catch((e: Error) => !ctl.signal.aborted && setError(e))
    return () => ctl.abort()
  }, [key]) // eslint-disable-line react-hooks/exhaustive-deps

  const shown = ids.length > 0 ? look : undefined
  const records = useMemo(() => {
    const rs = [...(shown?.records ?? [])].filter((r) => ids.includes(r.id))
    rs.sort((a, b) => (a.at === b.at ? a.id.localeCompare(b.id) : a.at < b.at ? -1 : 1) * (asc ? 1 : -1))
    return rs
  }, [shown, ids, asc])

  // Coverage over the stretch the selection covers, so a quiet or partial stretch is named.
  const first = records.reduce<string>((m, r) => (m === '' || r.at < m ? r.at : m), '')
  const last = records.reduce<string>((m, r) => (r.at > m ? r.at : m), '')
  const tooLong = !!first && Date.parse(last) - Date.parse(first) > 399 * 86400e3
  useEffect(() => {
    if (!first || tooLong) return
    const from = new Date(Date.parse(first) - 1000).toISOString()
    const to = new Date(Date.parse(last) + 1000).toISOString()
    getJSON<ExplorerContext>(`/api/v1/explorer/context${query({ from, to })}`).then(setCtx).catch(() => setCtx(undefined))
  }, [first, last, tooLong])
  const ctxNow = first && !tooLong ? ctx : undefined

  const missing = shown?.missing.filter((m) => ids.includes(m)) ?? []
  const warnings: string[] = []
  if (missing.length) warnings.push(tn(missing.length, '{n} selected record is no longer held (removed by retention) and cannot be shown or exported.', '{n} selected records are no longer held (removed by retention) and cannot be shown or exported.'))
  if (tooLong) warnings.push(t('The selection spans more than 400 days, so coverage was not checked.'))
  for (const b of ctxNow?.bands ?? []) {
    if (b.kind === 'outside_retention') warnings.push(t('The selection reaches back before the history OpsTide keeps.'))
    if (b.kind === 'before_first_observation') warnings.push(t('The selection reaches back before OpsTide first observed this cluster.'))
  }
  const gapped = [...new Set((ctxNow?.scopes ?? []).filter((s) => s.state === 'gap').map((s) => s.label))]
  if (gapped.length) warnings.push(t('Not watched for part of the selected period: {scopes}. Records from those stretches may be missing.', { scopes: gapped.join(', ') }))
  for (const s of ctxNow?.scopes ?? []) {
    const who = s.namespace ? `${s.label} (${s.namespace})` : s.label
    if (s.state === 'unavailable') warnings.push(s.problem ? t('{who} cannot be read right now ({problem}).', { who, problem: s.problem }) : t('{who} cannot be read right now.', { who }))
    if (s.state === 'disabled') warnings.push(t('{who} observation is off.', { who }))
  }

  const remove = (id: string) => removeFromWorkspace([id])
  const doExport = async () => {
    setBusy(true)
    try {
      await downloadPost('/api/v1/explorer/export', { ids, includeMessages: msgs }, 'opstide-explorer-selected.json')
    } catch (e) {
      setError(e as Error)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="page" style={{ gap: 18, paddingTop: 24 }}>
      <div className="head-row">
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">{t('Investigation Workspace')}</span>
          <h1 className="title" style={{ fontSize: 28 }}>{t('Records you picked')}</h1>
        </div>
        <span className="grow" />
        <ZoneSelect tz={tz} setTz={setTz} />
        <Link href="/explorer" className="button" style={{ display: 'inline-flex', alignItems: 'center' }}>{t('Back to Explorer')}</Link>
      </div>
      <div className="muted" style={{ margin: 0, maxWidth: 820 }}>
        {t('Kept in this tab only; nothing is saved on the server. Up to {max} records.', { max: WORKSPACE_MAX })} <Note more={t('OpsTide counts what is here and says where its watching was not whole; it does not tell you what it means.')}>{t('Counts, not conclusions')}</Note>
      </div>
      {error && <Failed error={error} />}

      {ids.length === 0 ? (
        <div className="empty-box no_matches">
          <b>{t('The Workspace is empty.')}</b>
          <span className="muted">{fill(t('Add records from the {explorer}, a release, a workload, a Node, a run, or the Log with the + button beside a record.'), { explorer: <Link href="/explorer">{t('Explorer')}</Link> })}</span>
        </div>
      ) : (
        <>
          <section aria-label={t('Inventory')} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <h2 className="label section-title">{t('Inventory')}</h2>
            <p style={{ margin: 0 }}>
              {fill(t('{n} selected'), { n: <b>{ids.length}</b> })} · {fill(t('{n} held'), { n: <b>{records.length}</b> })}{missing.length > 0 && <> · {fill(t('{n} no longer held'), { n: <b>{missing.length}</b> })}</>}
              {first && <> · {fill(t('from {from} to {to}'), { from: <span className="mono">{isoDate(first, tz)} {hhmmss(first, tz)}</span>, to: <span className="mono">{isoDate(last, tz)} {hhmmss(last, tz)}</span> })}</>}
            </p>
            <div className="two" style={{ gap: 24, gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))' }}>
              <Counts title={t('Kind of record')} rows={tally(records, (r) => groupWords[r.group] ?? r.group)} />
              <Counts title={t('Provenance')} rows={tally(records, (r) => provWord(r.provenance))} />
              <Counts title={t('Resource kind')} rows={tally(records, (r) => r.resource.kind || t('none named'))} />
              <Counts title={t('Namespace')} rows={tally(records, (r) => r.resource.namespace || t('cluster-wide'))} />
              <Counts title={t('Source')} rows={tally(records, (r) => r.source)} />
            </div>
          </section>

          <section aria-label={t('Coverage warnings')} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <h2 className="label section-title">{t('Where OpsTide’s watching was not whole')}</h2>
            {warnings.length === 0
              ? <p className="muted" style={{ margin: 0 }}>{ctxNow ? t('Every watch was whole over the period the selection covers.') : t('Checking…')}</p>
              : <ul style={{ margin: 0, paddingLeft: 18 }}>{warnings.map((w, i) => <li key={i}>{w}</li>)}</ul>}
          </section>

          <section aria-label={t('Selected records')} style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <div style={{ display: 'flex', gap: 12, alignItems: 'baseline', flexWrap: 'wrap' }}>
              <h2 className="label section-title" style={{ flex: 1 }}>{t('Selected records')}</h2>
              <button type="button" className="button" onClick={() => setAsc(!asc)}>{asc ? t('Oldest first') : t('Newest first')}</button>
            </div>
            <div className="table-wrap">
              <table className="data compact" style={{ minWidth: 640 }}>
                <thead><tr><th>{t('Time')}</th><th>{t('Record')}</th><th>{t('Resource')}</th><th>{t('Provenance')}</th><th><span className="sr">{t('Actions')}</span></th></tr></thead>
                <tbody>
                  {records.map((r) => (
                    <tr key={r.id}>
                      <td className="mono" style={{ whiteSpace: 'nowrap' }}>{isoDate(r.at, tz).slice(5)} {hhmmss(r.at, tz)}</td>
                      <td><span className="label" style={{ display: 'block' }}>{typeWord(r.type)}</span>{r.summary}</td>
                      <td className="mono" style={{ overflowWrap: 'anywhere' }}>{r.resource.name ? `${r.resource.kind} ${r.resource.namespace ? `${r.resource.namespace}/` : ''}${r.resource.name}` : <span className="muted">{t('none named')}</span>}</td>
                      <td><span className={`chip prov-${r.provenance.toLowerCase()}`} style={{ margin: 0 }} title={r.source}>{provWord(r.provenance)}</span></td>
                      <td style={{ whiteSpace: 'nowrap' }}>
                        <Link href={explorerLink({ rec: r.id, from: new Date(Date.parse(r.at) - 3600e3).toISOString(), to: new Date(Date.parse(r.at) + 3600e3).toISOString() })}>{t('Inspect')}</Link>{' '}
                        <button type="button" className="ws-toggle" onClick={() => remove(r.id)} aria-label={t('Remove {name} from the Workspace', { name: r.summary })}>✕</button>
                      </td>
                    </tr>
                  ))}
                  {missing.map((m) => (
                    <tr key={m}>
                      <td className="muted" colSpan={4}>{fill(t('Record {id} is no longer held (removed by retention).'), { id: <span className="mono">{m}</span> })}</td>
                      <td><button type="button" className="ws-toggle" onClick={() => remove(m)} aria-label={t('Remove {name} from the Workspace', { name: m })}>✕</button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <span className="muted" style={{ fontSize: 12 }}>{t('Ordered by record time, then id. Times in {tz}.', { tz })}</span>
          </section>

          <section aria-label={t('Export and clear')} style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <h2 className="label section-title">{t('Export the selection')}</h2>
            <label style={{ display: 'flex', gap: 8, alignItems: 'flex-start', maxWidth: 720 }}>
              <input type="checkbox" checked={msgs} onChange={(e) => setMsgs(e.target.checked)} />
              <span>{t('Include Kubernetes Event messages and quoted condition messages')}{msgs && <b className="fail">{t(' — they can contain names, addresses or other sensitive text, and go into the file.')}</b>}</span>
            </label>
            <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'center' }}>
              <button type="button" className="button" disabled={busy || records.length === 0} onClick={doExport}>{busy ? t('Preparing…') : t('Download selected as JSON')}</button>
              {!confirmClear ? <button type="button" className="button" onClick={() => setConfirmClear(true)}>{t('Clear the Workspace…')}</button> : (
                <span style={{ display: 'inline-flex', gap: 10, alignItems: 'center' }}>
                  {t('Remove all {n} from this tab?', { n: ids.length })}
                  <button type="button" className="button" onClick={() => { clearWorkspace(); setConfirmClear(false) }}>{t('Yes, clear')}</button>
                  <button type="button" className="button" onClick={() => setConfirmClear(false)}>{t('Cancel')}</button>
                </span>
              )}
            </div>
          </section>
        </>
      )}
    </div>
  )
}
