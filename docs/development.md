# Demo and development

## Try it

`make demo-local` (or `go run ./cmd/opstide -demo`) serves a made-up history from a temporary database: CronJobs
with a failed run and an unwatched hour, a nightly backup that has taken about 40% longer for five nights and a weekly
one with too few runs to compare, a Deployment's three releases, a StatefulSet held at its partition,
a DaemonSet, a Node that was recreated under the same name, an api Pod that restarted shortly after its Deployment's spec and its ConfigMap were written, Kubernetes Events, a collection gap and an object
OpsTide gave up on, a daily maintenance window over a failed run and an earlier api restart, and images that change
(api 1.5.0 to 1.5.1) and that two workloads share. It needs no cluster, kubeconfig or Docker, collects nothing, and says so on every page. It is
a snapshot, not a live view; to see real watching use kind below.

## Try it on kind

```bash
make demo-local   # no cluster, no Docker: made-up data in a temporary database, http://127.0.0.1:8080
make demo-up      # kind cluster "opstide-demo" with demo CronJobs (needs Docker and kind)
make demo         # OpsTide against it; open http://127.0.0.1:8080
make demo-rollout # a Deployment rollout to see on the Overview
make demo-down    # delete the cluster
```

The demo CronJobs run every 2–5 minutes, so the screens fill within a
quarter of an hour: payment reconciliation fails every tenth minute,
inventory sync sometimes runs long, one CronJob is suspended and one has
no description. They only sleep and exit. Stop `make demo` for a few
minutes and start it again to see a collection gap.

## Development

```bash
make web                     # build the UI (needs Node 22) into the binary's embed folder
make test
make run                     # your kubeconfig's current context, data in ./data
make run CONTEXT=kind-dev    # another context
```

For UI work, run `npm run dev` in `web/` next to `make run`: Vite serves
the app with hot reload and proxies `/api` to the collector.

`npm test` in `web/` also draws every screen, in English and in Turkish,
against API answers recorded from the demo (`web/src/test/demo-api.json`); a
screen fails if it throws, logs a React error or says it could not load. When
the API changes, record them again with a demo running:

```bash
go run ./cmd/opstide -demo -listen 127.0.0.1:18400 &
cd web && node scripts/record-ui-fixtures.cjs http://127.0.0.1:18400 src/test/demo-api.json
```

`-public-url` (`OPSTIDE_PUBLIC_URL`) is the address people open OpsTide at, for the links in Grafana annotations;
empty gives paths.

Probes: `/healthz` (process), `/readyz` (watches synced and storage
writable). They, the page and its scripts need no token; everything else does
when one is set (`-token-file`, or `OPSTIDE_TOKEN` for a quick try). Listening
on a non-loopback address without one logs a warning.

## README screenshots

The images in `docs/images/` are taken from the demo; to take them again (needs Playwright and Python's Pillow):

```bash
go run ./cmd/opstide -demo -listen 127.0.0.1:18437 &
node web/scripts/readme-screenshots.cjs http://127.0.0.1:18437
```

## Website

The project's website is static files in [website/](../website/) (English and Turkish, no external requests):
`make website-serve` previews it on http://127.0.0.1:8000. See [website/README.md](../website/README.md) for
refreshing its screenshots from the demo and deploying it.
