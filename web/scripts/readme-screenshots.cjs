#!/usr/bin/env node
// Captures the screenshots in docs/images/ (used by README.md) from a running demo.
//
//   go run ./cmd/opstide -demo -listen 127.0.0.1:18437 &
//   node web/scripts/readme-screenshots.cjs http://127.0.0.1:18437
//
// Needs Playwright with Chromium, and Python's Pillow to write WebP (otherwise
// the PNGs are kept and README.md must point at them).
'use strict'
const fs = require('fs')
const path = require('path')
const { execFileSync } = require('child_process')
const { chromium } = require('playwright')

const base = (process.argv[2] || 'http://127.0.0.1:18437').replace(/\/$/, '')
const out = path.resolve(__dirname, '..', '..', 'docs', 'images')
const WIDTH = 1600 // sharp on a README, small enough for the repository

async function failureRecord() {
  const res = await fetch(`${base}/api/v1/explorer?range=24h&limit=200`)
  const list = await res.json()
  const rec = list.records.find((r) => r.type === 'pod.restarted' && /OOMKilled/.test(r.summary || ''))
    || list.records.find((r) => r.type === 'job.failed')
  if (!rec) throw new Error('no failure-like record in the demo')
  return rec.id
}

async function shoot(browser, { theme, url, file, waitFor, onboarded = true }) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, colorScheme: theme, reducedMotion: 'reduce' })
  await ctx.addInitScript(([th, ob]) => {
    localStorage.setItem('opstide.lang', 'en')
    if (ob) localStorage.setItem('opstide.onboarded', '1')
    localStorage.setItem('opstide.tz', 'Europe/Istanbul')
    localStorage.setItem('opstide.theme', th)
  }, [theme, onboarded])
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
    'import os, sys',
    'from PIL import Image',
    'src, dst, w = sys.argv[1], sys.argv[2], int(sys.argv[3])',
    'im = Image.open(src).convert("RGB")',
    'im = im.resize((w, round(im.height * w / im.width)), Image.LANCZOS)',
    'q = 82',
    'while True:',
    '    im.save(dst, "WEBP", quality=q, method=6)',
    '    if os.path.getsize(dst) <= 220_000 or q <= 50: break',
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
  const rec = await failureRecord()
  const browser = await chromium.launch()
  try {
    const shots = [
      { theme: 'light', file: 'overview-light', url: '/' },
      { theme: 'dark', file: 'overview-dark', url: '/' },
      { theme: 'dark', file: 'explorer-before', url: `/explorer?range=24h&rec=${encodeURIComponent(rec)}`, waitFor: '[role="tabpanel"], [role="dialog"], aside' },
      { theme: 'light', file: 'schedules', url: '/schedules' },
      { theme: 'light', file: 'operation', url: '/operations/cj-backup' },
      { theme: 'light', file: 'release', url: '/releases/rel-api-6' },
    ]
    for (const s of shots) toWebp(await shoot(browser, s))
  } finally {
    await browser.close()
  }
})().catch((e) => { console.error(e); process.exit(1) })
