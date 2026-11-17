# Contributing to OpsTide

Thanks for looking. Issues and pull requests are welcome; for anything larger than a fix, open an issue first so we can
agree on the shape before you spend the time.

## What OpsTide will and will not do

A change has to keep these, because they are why people can install it:

- **Read-only.** The ServiceAccount has `list` and `watch`, nothing else. No writes to the cluster, no Secret data, no
  logs, no exec.
- **Says how it knows.** Every record is observed, derived or unknown. A rule that derives something says so, and
  claims nothing about a time OpsTide was not watching.
- **Before, not because.** It shows what happened and what happened earlier, never why.
- **Private by default.** Nothing leaves the cluster unless the person installing it turns that on.

## Getting started

You need Go (the version in `go.mod`) and Node 22.

```bash
make demo-local        # made-up history, no cluster needed: http://127.0.0.1:8080
make run               # against your kubeconfig's current context
cd web && npm run dev  # the UI with hot reload, next to make run
```

[docs/development.md](docs/development.md) has the kind demo, probes and the rest.

## Before you open a pull request

```bash
make test              # Go
make lint              # gofmt, golangci-lint, ESLint, TypeScript
cd web && npm test     # draws every screen in English and Turkish
```

And a few rules the tests enforce or reviewers will ask about:

- **Every word the UI draws goes through `t()`**, with its Turkish in `web/src/i18n/tr-*.ts`. A sentence the API sends
  for the UI to show is translated from `tr-server.ts`. Placeholders must match.
- **Schema migrations only go forward.** Append one to `internal/store/schema.go`; never edit an existing one. OpsTide
  copies the database before migrating it, and an older build refuses a newer database.
- **API changes need the UI fixtures recorded again**, so the screen tests draw what the server now sends:

  ```bash
  go run ./cmd/opstide -demo -listen 127.0.0.1:18400 &
  cd web && node scripts/record-ui-fixtures.cjs http://127.0.0.1:18400 src/test/demo-api.json
  ```

- **A new permission in the chart** needs a line in [docs/install.md](docs/install.md) saying what it grants and why.
- **A new metric** goes in [docs/metrics.md](docs/metrics.md), a new endpoint or field in [docs/api.md](docs/api.md).

## Commit messages

A subject in the imperative that says what changes for someone using OpsTide ("Say when a CronJob is overdue for a
success"), then a body that says why and what is deliberately left out.

## Security

Please report vulnerabilities privately, as [SECURITY.md](SECURITY.md) describes, not in an issue.

## License

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE), like the rest
of OpsTide.
