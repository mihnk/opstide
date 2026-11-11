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

  const pick = (kind) => wl.find((x) => x.kind === kind)
  const pages = ['/', '/welcome', '/explorer?range=24h', '/explorer?range=24h&preset=changes', '/workloads', '/releases', '/schedules', '/system', '/workspace']
  for (const x of [pick('StatefulSet'), pick('Deployment'), pick('DaemonSet')].filter(Boolean)) {
    for (const t of ['overview', 'history', 'revisions', 'pods', 'evidence']) pages.push(`/workloads/${encodeURIComponent(x.id)}?tab=${t}`)
  }
  for (const r of rels.slice(0, 2)) {
    for (const t of ['overview', 'timeline', 'pods', 'evidence', 'changes', 'context']) pages.push(`/releases/${encodeURIComponent(r.id)}?tab=${t}`)
  }
  for (const n of nodes.slice(0, 2)) pages.push(`/nodes/${encodeURIComponent(n.id)}`)
  const op = ops[0]
  if (op) {
    pages.push(`/operations/${encodeURIComponent(op.id)}`)
    const detail = await j(`/api/v1/operations/${encodeURIComponent(op.id)}?tz=UTC&runs=20`)
    for (const r of (detail.runs || []).slice(0, 3)) pages.push(`/operations/${encodeURIComponent(op.id)}?run=${encodeURIComponent(r.id)}`)
  }
  // One fixed window for the record pages, so they share one recorded list instead of one per visit.
  const to = new Date(Date.parse(opsAnswer.now)).toISOString()
  const from = new Date(Date.parse(opsAnswer.now) - 24 * 3600e3).toISOString()
  for (const r of recs.slice(0, 30)) pages.push(`/explorer?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}&rec=${encodeURIComponent(r.id)}`)

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
  console.log(Object.keys(api).length, 'answers,', pages.length, 'pages,', (fs.statSync(out).size / 1024).toFixed(0), 'KB')
})()
