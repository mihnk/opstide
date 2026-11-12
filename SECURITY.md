# Security

OpsTide runs inside clusters and reads them, so reports are welcome.

## Reporting a vulnerability

Please report it privately through GitHub: the repository's **Security** tab →
**Report a vulnerability**. Do not open a public issue. Say what you found, how to
reproduce it, and which version or commit you tested.

You will get an answer within a week. A fix is released before the details are
made public, and you are credited unless you ask not to be.

## Supported versions

OpsTide is in early development (v0.1). Only the latest commit on `main`, and from
the first release on the latest release, gets fixes.

## What OpsTide is designed not to do

- Change the cluster: its ServiceAccount has only `list` and `watch`.
- Request a Secret's data, logs or exec. Secret metadata is read only when
  `watch.secretMetadata` is on, and the RBAC that needs is described in
  [docs/configuration-changes.md](docs/configuration-changes.md#configmaps-and-secrets).
- Keep ConfigMap values: they are replaced by keyed hashes as they arrive.
- Send anything out of the cluster.

A way to make it do any of these is a vulnerability.
