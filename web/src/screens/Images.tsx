import { useEffect, useState } from 'react'
import { imageRef, query, type ImageDetail, type ImageGroup, type ImageList, type ImageUse } from '../api'
import { Failed, Loading } from '../components/Shell'
import { Note } from '../components/Note'
import { provWord } from '../explorer'
import { t, tn } from '../i18n'
import { useApi } from '../live'
import { Link, navigate, withParams } from '../router'
import { offset, relativeTime, utcToZoned, zonedToUtc } from '../time'

// The image inventory: which container images the workloads OpsTide records ran, where, since when and until when,
// read from the revisions it already holds. A past time says whether OpsTide was watching then; nothing is guessed.

// English keys; drawn through t().
const sinceWords: Record<ImageUse['sinceBasis'], string> = {
  created: 'when the revision was created, as Kubernetes reported it',
  observed: 'when OpsTide saw it become current',
  first_recorded: 'when OpsTide first recorded it; it was in use before that',
}
const untilWords: Record<NonNullable<ImageUse['untilBasis']>, string> = {
  replaced: 'replaced by a newer revision',
  container_removed: 'the container was removed',
  workload_deleted: 'the workload was deleted',
}
const coverageWords: Record<ImageList['coverage']['state'], string> = {
  watching: 'watching', gap: 'gap', before_first_observation: 'before first observation', outside_retention: 'outside retention',
}

function UseWhen({ u, now, tz }: { u: ImageUse; now: string; tz: string }) {
  return (
    <>
      <span className="mono" title={t(sinceWords[u.sinceBasis])}>{relativeTime(u.since, now, tz)}{u.sinceBasis === 'first_recorded' ? ' *' : ''}</span>
      <span className="muted"> → </span>
      {u.until
        ? <span className="mono" title={u.untilBasis ? t(untilWords[u.untilBasis]) : undefined}>{relativeTime(u.until, now, tz)}</span>
        : <span className="muted">{t('still current')}</span>}
    </>
  )
}

function WorkloadCell({ u }: { u: ImageUse }) {
  return (
    <>
      <span className="label">{u.kind}{u.adopted ? ` · ${t('adopted')}` : ''}</span>
      <Link href={u.path} className="mono" style={{ display: 'block', overflowWrap: 'anywhere' }}>{u.namespace}/{u.workload}</Link>
    </>
  )
}

function RevisionCell({ u }: { u: ImageUse }) {
  const rev = u.fromRevision === u.toRevision ? u.fromRevision : `${u.fromRevision} → ${u.toRevision}`
  if (!rev) return <span className="muted">—</span>
  return u.latestReleaseId
    ? <Link href={`/releases/${encodeURIComponent(u.latestReleaseId)}`} className="mono">{rev}</Link>
    : <span className="mono" style={{ overflowWrap: 'anywhere' }}>{rev}</span>
}

function ContainerCell({ u }: { u: ImageUse }) {
  return (
    <>
      <span className="mono">{u.container}</span>
      {u.init && <span className="chip">{t('init')}</span>}
      {u.reportedImageId && <span className="mono muted" style={{ display: 'block', fontSize: 11, overflowWrap: 'anywhere' }} title={t('The image ID a Pod of this revision reported')}>{u.reportedImageId}</span>}
    </>
  )
}

export function Images({ params, tz }: { params: URLSearchParams; tz: string }) {
  const at = params.get('at') ?? ''
  const q = params.get('q') ?? ''
  const ns = params.get('namespace') ?? ''
  const { data, error } = useApi<ImageList>(`/api/v1/images${query({ at, namespace: ns, q })}`, { keep: true })
  const [text, setText] = useState(q)
  const [seenQ, setSeenQ] = useState(q)
  if (q !== seenQ) {
    setSeenQ(q)
    setText(q)
  }
  useEffect(() => {
    const timer = window.setTimeout(() => {
      if (text !== q) navigate(withParams(params, { q: text || null }), true)
    }, 400)
    return () => window.clearTimeout(timer)
  }, [text, q, params])
  const set = (change: Record<string, string | null>) => navigate(withParams(params, change), true)
  const [edit, setEdit] = useState<string | null>(null)
  const shown = edit ?? (at ? utcToZoned(Date.parse(at), tz) : data ? utcToZoned(Date.parse(data.at), tz) : '')
  const apply = () => {
    const ms = zonedToUtc(shown, tz)
    if (isNaN(ms)) return
    setEdit(null)
    set({ at: new Date(ms).toISOString().replace('.000Z', 'Z') })
  }
  const uses = data?.images.reduce((n, g) => n + g.uses.length, 0) ?? 0

  return (
    <div className="page" style={{ gap: 18 }}>
      <div className="head-row" style={{ gap: '12px 28px' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          <span className="label">{t('Container images in the workloads OpsTide records')}</span>
          <h1 className="title">{data ? tn(data.total, '{n} image', '{n} images') : t('Images')}</h1>
          {data && <span className="muted" style={{ fontSize: 13 }}>{data.current ? t('Now') : t('At {time}', { time: relativeTime(data.at, data.now, tz) })} · {tn(uses, '{n} container', '{n} containers')}</span>}
        </div>
        <span className="grow" />
        <Link href="/workloads" style={{ fontSize: 13 }}>{t('Workloads')}</Link>
      </div>

      <form className="filters" aria-label={t('Filter images')} onSubmit={(e) => { e.preventDefault(); apply() }}>
        <label className="field"><span className="label">{t('Find')}</span>
          <input className="control" style={{ width: 240 }} value={text} placeholder={t('an image, or a workload')} onChange={(e) => setText(e.target.value)} />
        </label>
        <label className="field"><span className="label">{t('Namespace')}</span>
          <input key={ns} className="control" style={{ width: 150 }} defaultValue={ns} placeholder={t('all')}
            onBlur={(e) => e.target.value.trim() !== ns && set({ namespace: e.target.value.trim() || null })}
            onKeyDown={(e) => e.key === 'Enter' && (e.target as HTMLInputElement).blur()} />
        </label>
        <label className="field"><span className="label">{t('At ({tz} {offset})', { tz, offset: offset(tz) })}</span>
          <input className="control mono" type="datetime-local" value={shown} onChange={(e) => setEdit(e.target.value)} />
        </label>
        <button type="button" className="button" onClick={apply} disabled={isNaN(zonedToUtc(shown, tz))}>{t('Apply')}</button>
        <button type="button" className="button" aria-pressed={!at} onClick={() => { setEdit(null); set({ at: null }) }}>{t('Now')}</button>
        <span className="grow" style={{ flex: 1 }} />
        {(q || ns || at) && <Link href="/images" replace style={{ fontSize: 13 }}>{t('Clear the filters')}</Link>}
      </form>

      {data && (data.coverage.state !== 'watching' || data.notes.length > 0) && (
        <div className="banner" role="note">
          <span className="label">{t(coverageWords[data.coverage.state])}</span>
          <span>{data.notes.map((n, i) => <span key={i} style={{ display: 'block' }}>{n}</span>)}</span>
        </div>
      )}
      {error && !data && <Failed error={error} />}
      {!data && !error && <Loading />}
      {data && data.images.length === 0 && (
        <div className="empty">{q || ns ? t('No image matches these filters at this time.') : t('No image is recorded at this time. That says what OpsTide holds, not that nothing ran.')}</div>
      )}
      {data && data.images.length > 0 && (
        <div className="table-wrap">
          <table className="data" style={{ minWidth: 1040 }}>
            <thead><tr><th>{t('Image')}</th><th>{t('Workload')}</th><th>{t('Container')}</th><th>{t('Since → until')}</th><th>{t('Revision')}</th><th>{t('Provenance')}</th></tr></thead>
            <tbody>
              {data.images.map((g) => <ImageRows key={g.image} g={g} now={data.now} tz={tz} />)}
            </tbody>
          </table>
        </div>
      )}
      {data?.truncated && <span className="muted" style={{ fontSize: 13 }}>{t('Only the first {n} images are listed; narrow the search.', { n: data.images.length })}</span>}
      <Note block more={t('Times are those of each workload\'s revisions: when the revision that gave a container its image became current, and when a newer one replaced it, not when each Pod pulled it. Deployments come from the ReplicaSet templates OpsTide saw (observed); StatefulSets and DaemonSets from their ControllerRevisions (derived: Pods held back by a partition or OnDelete can still run an earlier one); CronJobs from their job template. A time marked * is when OpsTide first recorded it: it was in use before. Image references are shown as written; a missing tag is not filled in.')}>{t('From recorded revisions · not a scan of the nodes')}</Note>
    </div>
  )
}

function ImageRows({ g, now, tz }: { g: ImageGroup; now: string; tz: string }) {
  return (
    <>
      {g.uses.map((u, i) => (
        <tr key={`${u.workloadId}/${u.init}/${u.container}/${u.since}`}>
          {i === 0 && (
            <td rowSpan={g.uses.length} style={{ verticalAlign: 'top' }}>
              <Link href={`/images/${g.ref}`} className="mono" style={{ display: 'block', overflowWrap: 'anywhere', fontWeight: 500 }}>{g.image}</Link>
              <span className="muted" style={{ fontSize: 12 }}>{tn(new Set(g.uses.map((x) => x.workloadId)).size, '{n} workload', '{n} workloads')} · {g.namespaces.join(', ')}</span>
            </td>
          )}
          <td><WorkloadCell u={u} /></td>
          <td><ContainerCell u={u} /></td>
          <td><UseWhen u={u} now={now} tz={tz} /></td>
          <td><RevisionCell u={u} /></td>
          <td><span className={`chip prov-${u.provenance.toLowerCase()}`} style={{ marginLeft: 0 }} title={u.note}>{provWord(u.provenance)}</span></td>
        </tr>
      ))}
    </>
  )
}

export function Image({ refId, tz }: { refId: string; tz: string }) {
  const { data, error } = useApi<ImageDetail>(`/api/v1/images/${refId}`)
  if (error && !data) return <div className="page"><Failed error={error} /></div>
  if (!data) return <div className="page"><Loading /></div>
  return (
    <div className="page" style={{ gap: 18 }}>
      <div className="head-row" style={{ gap: '12px 28px' }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
          <span className="label">{t('Image')}</span>
          <h1 className="title mono" style={{ overflowWrap: 'anywhere' }}>{data.image}</h1>
          <span className="muted" style={{ fontSize: 13 }}>
            {t('repository')} <span className="mono">{data.repository}</span>
            {data.tag ? <> · {t('tag')} <span className="mono">{data.tag}</span></> : <> · {t('no tag written')}</>}
            {data.digest && <> · <span className="mono">{data.digest}</span></>}
          </span>
        </div>
        <span className="grow" />
        <Link href="/images" style={{ fontSize: 13 }}>{t('All images')}</Link>
      </div>

      <p className="sentence" style={{ margin: 0 }}>
        {data.uses.length === 0 ? t('OpsTide holds no record of this image.') : t('{current} of {uses} recorded uses are current, in {workloads} workloads.', { current: data.current, uses: data.uses.length, workloads: data.workloads })}
      </p>
      {data.notes.length > 0 && <div className="banner" role="note"><span className="label">{t('Note')}</span><span>{data.notes.map((n, i) => <span key={i} style={{ display: 'block' }}>{n}</span>)}</span></div>}

      {data.uses.length > 0 && (
        <div className="table-wrap">
          <table className="data" style={{ minWidth: 980 }}>
            <thead><tr><th>{t('Workload')}</th><th>{t('Container')}</th><th>{t('Since → until')}</th><th>{t('Before and after')}</th><th>{t('Revision')}</th><th>{t('Provenance')}</th></tr></thead>
            <tbody>
              {data.uses.map((u) => (
                <tr key={`${u.workloadId}/${u.init}/${u.container}/${u.since}`}>
                  <td><WorkloadCell u={u} /></td>
                  <td><ContainerCell u={u} /></td>
                  <td><UseWhen u={u} now={data.now} tz={tz} />{u.untilBasis && <span className="muted" style={{ display: 'block', fontSize: 12 }}>{t(untilWords[u.untilBasis])}</span>}</td>
                  <td style={{ fontSize: 13 }}>
                    {u.previous && <span style={{ display: 'block' }}><span className="muted">{t('before:')}</span> <Link href={`/images/${imageRef(u.previous)}`} className="mono">{u.previous}</Link></span>}
                    {u.replacedBy && <span style={{ display: 'block' }}><span className="muted">{t('after:')}</span> <Link href={`/images/${imageRef(u.replacedBy)}`} className="mono">{u.replacedBy}</Link></span>}
                    {!u.previous && !u.replacedBy && <span className="muted">—</span>}
                  </td>
                  <td><RevisionCell u={u} /></td>
                  <td><span className={`chip prov-${u.provenance.toLowerCase()}`} style={{ marginLeft: 0 }} title={u.note}>{provWord(u.provenance)}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {data.otherTags.length > 0 && (
        <section aria-label={t('Other images of this repository')} className="rows">
          <h2 className="label section-title">{t('Other images of this repository')}</h2>
          {data.otherTags.map((o) => (
            <Link key={o.image} className="row" href={`/images/${o.ref}`} style={{ gridTemplateColumns: 'minmax(0, 1fr) auto' }}>
              <span className="mono" style={{ overflowWrap: 'anywhere' }}>{o.image}</span>
              <span className="muted" style={{ fontSize: 12 }}>{t('{current} current · {uses} recorded', { current: o.current, uses: o.uses })}</span>
            </Link>
          ))}
        </section>
      )}
    </div>
  )
}
