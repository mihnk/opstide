#!/usr/bin/env node
// Captures the website's product screenshots from a running OpsTide demo.
//
//   go run ./cmd/opstide -demo -listen 127.0.0.1:18436 &
//   node website/scripts/screenshots.cjs http://127.0.0.1:18436
//
// Writes website/assets/img/{hero,overview,explorer}-{en,tr}.webp (PNG first,
// then converted with Python's Pillow when it is installed; otherwise the PNGs
// are kept and the HTML must point at them). Needs Playwright with Chromium
// (npm i -g playwright && npx playwright install chromium).
'use strict'
const fs = require('fs')
const path = require('path')
const { execFileSync } = require('child_process')
const { chromium } = require('playwright')

const base = (process.argv[2] || 'http://127.0.0.1:18436').replace(/\/$/, '')
const out = path.resolve(__dirname, '..', 'assets', 'img')
const WIDTH = 2400 // stored width: sharp at the largest size the page shows them, small enough to load fast

async function findRecord() {
  // A failure-like record with a "Before" tab: the api Pod restarted after an OOMKill.
  const res = await fetch(`${base}/api/v1/explorer?range=24h&limit=200`)
  const list = await res.json()
  const rec = list.records.find((r) => r.type === 'pod.restarted' && /OOMKilled/.test(r.summary || ''))
    || list.records.find((r) => r.type === 'job.failed')
  if (!rec) throw new Error('no failure-like record in the demo')
  return rec.id
}

async function shoot(browser, { lang, theme, url, file, waitFor }) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: theme, reducedMotion: 'reduce' })
  await ctx.addInitScript(([l, th]) => {
    localStorage.setItem('opstide.lang', l)
    localStorage.setItem('opstide.onboarded', '1')
    localStorage.setItem('opstide.tz', 'Europe/Istanbul')
    localStorage.setItem('opstide.theme', th)
  }, [lang, theme])
  const page = await ctx.newPage()
  await page.goto(base + url, { waitUntil: 'networkidle' })
  if (waitFor) await page.waitForSelector(waitFor, { timeout: 15000 })
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(800)
  const png = path.join(out, file + '.png')
  await page.screenshot({ path: png })
  await ctx.close()
  return png
}

function toWebp(png) {
  const webp = png.replace(/\.png$/, '.webp')
  const py = [
    'import sys',
    'from PIL import Image',
    'src, dst, w = sys.argv[1], sys.argv[2], int(sys.argv[3])',
    'im = Image.open(src).convert("RGB")',
    'im = im.resize((w, round(im.height * w / im.width)), Image.LANCZOS)',
    'q = 82',
    'while True:',
    '    im.save(dst, "WEBP", quality=q, method=6)',
    '    import os',
    '    if os.path.getsize(dst) <= 400_000 or q <= 50: break',
    '    q -= 6',
    'print(dst, im.size, os.path.getsize(dst), "q", q)',
  ].join('\n')
  try {
    process.stdout.write(execFileSync('python3', ['-I', '-c', py, png, webp, String(WIDTH)]).toString())
    fs.unlinkSync(png)
  } catch (e) {
    console.warn('Pillow not available, keeping', png)
  }
}

;(async () => {
  fs.mkdirSync(out, { recursive: true })
  const rec = await findRecord()
  const browser = await chromium.launch()
  try {
    for (const lang of ['en', 'tr']) {
      const shots = [
        { theme: 'dark', file: `hero-${lang}`, url: `/explorer?range=24h&rec=${encodeURIComponent(rec)}`, waitFor: '[role="tabpanel"], [role="dialog"], aside' },
        { theme: 'light', file: `overview-${lang}`, url: '/' },
        { theme: 'light', file: `explorer-${lang}`, url: '/explorer?range=24h&namespace=shop&severity=warning' },
      ]
      for (const s of shots) toWebp(await shoot(browser, { lang, ...s }))
    }
  } finally {
    await browser.close()
  }
})().catch((e) => { console.error(e); process.exit(1) })
