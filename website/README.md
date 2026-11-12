# OpsTide website

The marketing site: static files, no build step, no external requests (fonts are
self-hosted, there are no CDNs or analytics, and each page carries a
`Content-Security-Policy` that allows only `'self'`).

```
index.html            English (lang="en")
tr/index.html         Turkish (lang="tr")
404.html              Both languages
assets/site.css       The only stylesheet
assets/site.js        Copy buttons and the small-screen menu; the site works without it (only copying needs it)
assets/img/           Screenshots, {hero,overview,explorer}-{en,tr}.webp, 2400×1500
assets/fonts/         Archivo (variable, wdth) and JetBrains Mono 400/600, latin + latin-ext, SIL OFL 1.1 (OFL-*.txt)
favicon.svg, robots.txt
scripts/screenshots.cjs   Recaptures the screenshots from the demo
wrangler.jsonc, .assetsignore   Cloudflare Workers static assets
```

The pages are plain HTML: edit `index.html` and `tr/index.html` side by side and keep
their structure and claims the same. Paths are root-relative (`/assets/...`), so serve the
folder as a web root rather than opening the files directly.

## Preview locally

```bash
make website-serve            # from the repository root; http://127.0.0.1:8000
# or
cd website && python3 -m http.server 8000
```

## Refresh the screenshots

They come from the demo (`-demo`: made-up data, nothing collected), at 1440×900 with a
device scale factor of 2, in English and Turkish, with the time zone Europe/Istanbul:

- `hero-*`: dark theme, the Explorer with the api Pod's OOMKilled restart open on its *Before* tab;
- `overview-*`: light theme, the Overview;
- `explorer-*`: light theme, the Explorer filtered by `namespace=shop` and `severity=warning` (two tokens).

```bash
cd web && npm run build && cd ..                      # the UI embedded in the binary
go build -o /tmp/opstide-demo-site ./cmd/opstide
/tmp/opstide-demo-site -demo -listen 127.0.0.1:18436 &
node website/scripts/screenshots.cjs http://127.0.0.1:18436
kill %1
```

The script needs Playwright with Chromium (`npm i -g playwright && npx playwright install chromium`)
and converts the captures to WebP with Python's Pillow (`pip install pillow`), at 2400 px wide
and at most about 400 KB each; without Pillow it leaves PNGs, and the `src` attributes must
then be changed to `.png`. If a screenshot changes what it shows, update its `alt` text in
both pages.

## Deploy (Cloudflare Workers static assets)

`wrangler.jsonc` publishes this folder as static assets with no Worker script:
`404.html` answers unknown paths with a 404, and `.assetsignore` keeps `README.md`,
`wrangler.jsonc` and `scripts/` out of the upload.

```bash
cd website
npx wrangler login       # once
npx wrangler deploy
```

Then attach a custom domain to the `opstide-website` Worker in the Cloudflare dashboard.
Once the domain is known, make the Open Graph `og:url` and `og:image` and the `hreflang`
links absolute (`https://<domain>/...`) in both pages, and add a `Sitemap:` line to
`robots.txt` if you add a sitemap.
