// Records the API answers the UI tests draw against (src/test/demo-api.json).
// Start a demo (make demo-local, or: go run ./cmd/opstide -demo -listen 127.0.0.1:18400), then from web/:
//   node scripts/record-ui-fixtures.cjs http://127.0.0.1:18400 src/test/demo-api.json
// It visits every screen in a browser (Playwright, not a dependency of this package) and keeps the first answer to each
// request, keyed by path and query, written in key order so a re-recording diffs cleanly. recordedAt is the demo's
// "now"; the tests set their clock to it.
const { chromium } = require('playwright')
const fs = require('fs')

const base = process.argv[2]
const out = process.argv[3]
if (!base || !out) {
  console.error('usage: node scripts/record-ui-fixtures.cjs <demo url> <out.json>')
  process.exit(2)
}

;(async () => {
  const j = async (p) => (await fetch(base + p)).json()
  const wl = (await j('/api/v1/workloads?range=all')).workloads || []
  const rels = (await j('/api/v1/releases')).releases || []
  const opsAnswer = await j('/api/v1/operations?tz=UTC')
  const ops = opsAnswer.operations || []
  const recs = (await j('/api/v1/explorer?range=24h&limit=100')).records || []
  const nodes = (await j('/api/v1/nodes')).nodes || []
  const imgs = (await j('/api/v1/images')).images || []

  const pick = (kind) => wl.find((x) => x.kind === kind)
  // Every step of the guide: several draw live pictures from the record (the Overview's window, the Explorer, schedules).
  const guideSteps = Array.from({ length: 12 }, (_, i) => `/welcome?step=${i + 1}`)
  const pages = ['/', '/welcome', ...guideSteps, '/explorer?range=24h', '/explorer?range=24h&preset=changes', '/workloads', '/releases', '/schedules', '/system', '/workspace']
  for (const x of [pick('StatefulSet'), pick('Deployment'), pick('DaemonSet')].filter(Boolean)) {
    for (const t of ['overview', 'history', 'revisions', 'pods', 'evidence']) pages.push(`/workloads/${encodeURIComponent(x.id)}?tab=${t}`)
  }
  for (const r of rels.slice(0, 2)) {
    for (const t of ['overview', 'timeline', 'pods', 'evidence', 'changes', 'context']) pages.push(`/releases/${encodeURIComponent(r.id)}?tab=${t}`)
  }
  for (const n of nodes.slice(0, 2)) pages.push(`/nodes/${encodeURIComponent(n.id)}`)
  // The image inventory now, at a time in the past (the api's previous image was current then), and the image that runs
  // in the most workloads (its history and the other tags of its repository).
  const pastAt = new Date(Date.parse(opsAnswer.now) - 30 * 3600e3).toISOString().replace('.000Z', 'Z')
  pages.push('/images', `/images?at=${encodeURIComponent(pastAt)}`)
  const shared = [...imgs].sort((a, b) => b.uses.length - a.uses.length)[0]
  if (!shared || shared.uses.length < 2) {
    console.error('no image runs in two workloads: the demo no longer shows a shared image')
    process.exit(1)
  }
  pages.push(`/images/${shared.ref}`)
  const apiImage = imgs.find((x) => x.image.startsWith('shop/api:'))
  if (apiImage) pages.push(`/images/${apiImage.ref}`)
  const op = ops[0]
  if (op) {
    pages.push(`/operations/${encodeURIComponent(op.id)}`)
    const detail = await j(`/api/v1/operations/${encodeURIComponent(op.id)}?tz=UTC&runs=20`)
    for (const r of (detail.runs || []).slice(0, 3)) pages.push(`/operations/${encodeURIComponent(op.id)}?run=${encodeURIComponent(r.id)}`)
  }
  // A failed run of each CronJob (its panel links to "What changed before this").
  for (const o of ops) {
    const detail = await j(`/api/v1/operations/${encodeURIComponent(o.id)}?tz=UTC&runs=50`)
    const failed = (detail.runs || []).find((r) => r.status === 'failed')
    if (failed) pages.push(`/operations/${encodeURIComponent(o.id)}?run=${encodeURIComponent(failed.id)}`)
  }
  // One fixed window for the record pages, so they share one recorded list instead of one per visit.
  const to = new Date(Date.parse(opsAnswer.now)).toISOString()
  const from = new Date(Date.parse(opsAnswer.now) - 24 * 3600e3).toISOString()
  // The first 30 records, and every failure-like one of the last 24 h: their panels ask what changed before them.
  const failureLike = new Set(['job.failed', 'pod.restarted', 'pod.oomkilled', 'pod.evicted', 'pod.waiting', 'release.stalled', 'release.failed', 'release.unavailable', 'release.health.degraded', 'node.condition_changed'])
  const isFailure = (r) => failureLike.has(r.type) || (r.type === 'kubernetes.event' && r.eventType === 'Warning')
  const visited = [...recs.slice(0, 30), ...recs.slice(30).filter(isFailure)]
  for (const r of visited) pages.push(`/explorer?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}&rec=${encodeURIComponent(r.id)}`)
  const withChanges = []
  for (const r of visited.filter(isFailure)) {
    const b = await j(`/api/v1/explorer/records/${encodeURIComponent(r.id)}/before?window=1h`)
    if ((b.changes || []).length > 0) withChanges.push(r.id)
  }
  if (withChanges.length === 0) {
    console.error('no failure-like record of the last 24 h has a change in the hour before it: the demo no longer shows "What changed before this"')
    process.exit(1)
  }

  const b = await chromium.launch()
  const ctx = await b.newContext({ viewport: { width: 1280, height: 900 } })
  await ctx.addInitScript(() => {
    localStorage.setItem('opstide.lang', 'en')
    localStorage.setItem('opstide.onboarded', '1')
    localStorage.setItem('opstide.tz', 'UTC')
  })
  const got = {}
  const pending = []
  ctx.on('response', (res) => {
    const u = new URL(res.url())
    if (!u.pathname.startsWith('/api/') || u.pathname === '/api/v1/stream') return
    const key = u.pathname + u.search
    pending.push(res.json().then((body) => { if (!(key in got)) got[key] = body }, () => { /* not JSON: not kept */ }))
  })
  const page = await ctx.newPage()
  const visit = async (u) => {
    await page.goto(base + u)
    await page.waitForLoadState('networkidle').catch(() => {})
  }
  for (const u of pages) await visit(u)
  // The Workspace with records in it: its lookup is a POST, recorded like the rest.
  await page.evaluate((ids) => sessionStorage.setItem('opstide.workspace.v1', JSON.stringify(ids.map((id) => ({ id, addedAt: 0 })))), recs.slice(0, 30).map((r) => r.id))
  await visit('/workspace')
  await page.evaluate(() => sessionStorage.removeItem('opstide.workspace.v1'))
  await Promise.all(pending)
  await b.close()

  const api = {}
  for (const k of Object.keys(got).sort()) api[k] = got[k]
  fs.writeFileSync(out, JSON.stringify({ recordedFrom: 'opstide -demo', recordedAt: opsAnswer.now, pages, api }))
  console.log(Object.keys(api).length, 'answers,', pages.length, 'pages,', (fs.statSync(out).size / 1024).toFixed(0), 'KB;', withChanges.length, 'failure-like record(s) with changes before them')
})()
