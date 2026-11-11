# Migrations, recovery and releases

This milestone is a library plus a smoke command. It neither runs a server nor
provides a database query endpoint. Do not deploy the M1 command as a service.
The deployment milestone must mount the persistent directory only in the app,
with one replica, no query sidecar/service and no external DB MCP endpoint.
Secrets, issuer, subjects, groups, hostname, exposure, timezone and storage values
are private deployment configuration. There are no real values in this repo.

SQLite uses foreign keys, a 5-second busy timeout and one connection per Store.
The service is designed for one app process/replica, using a Store mutex around
permissions and operations; transactions cover multi-record changes and claims.
An atomic claim upsert also prevents two Store instances from winning one claim.
Mount local persistent storage with SQLite-compatible locking, not an arbitrary
shared network filesystem. Set restrictive directory/file ownership for the
nonroot image user; `Open` creates new directories 0700 and new files 0600, but
does not fix pre-existing directory/file permissions. Do not expose this directory
through HTTP static file serving. The timezone is mandatory; tests cover the
America/Chicago midnight boundary. The Go binary embeds the IANA timezone data.

## Schema and rollback

The current schema is version 1 (`PRAGMA user_version`). New databases get all
DDL and the version marker in a single transaction. Reopening version 1 is a
no-op. A newer schema is refused, rather than silently opened by older code.
Future schema changes must be ordered, transactional migrations with fixture
upgrade tests. Prefer additive changes compatible with the previous binary;
do not silently migrate backward, drop records or discard coordination history.

Before any future migration, stop the single writer, copy the **whole database
and any journal/WAL companions** into a private protected backup, and verify it
opens and passes an integrity check using a controlled recovery tool inside the
data boundary. Never upload backups or real content to this repository. Rehearse
recovery on a synthetic copy first. This milestone has no production data and
no backup/recovery endpoint.

For binary-only rollback on compatible schema, stop the writer and restore the
previous image digest. For incompatible migration rollback, keep the current
database intact, restore the pre-migration backup into a separate directory,
and start the prior binary against that copy. Verify identity/membership,
owner secrecy, buyer coordination, read-only archives and persistence before
switching traffic. Restoring an older snapshot loses later writes; obtain the
owner's decision before any destructive recovery. There are no automatic down
migrations or automated data deletion commands.

## CI and publication

Use Conventional Commit subjects, e.g. `feat: add list priorities` or
`fix: preserve claims on restore`. Release-please tracks `version.txt`,
`.release-please-manifest.json` and `CHANGELOG.md` with simple releases. The first
release starts from 0.0.0. Workflow changes require owner review.

CI runs the source boundary check with negative fixtures, formatting, vet, the
real SQLite tests under the race detector, repeated claim/differential checks,
and a container build. It triggers for pull requests, merge groups, and queue
branch pushes, with valid `check-<full-SHA>` tags; those builds never publish.
Every main push independently publishes `sha-<full-SHA>` and
`main-<UTC-commit-time>-<12-char-SHA>`. No moving `main` or `latest` tag is used.
The deployment can select the sortable main tags and pin a digest for rollback.
Release-please runs separately from main image publishing; a missing release
credential cannot prevent the main image job. On a release, the same workflow
checks out the returned version tag and publishes `v<major>.<minor>.<patch>`.
It does not rely on a second release event from GITHUB_TOKEN.

The repository owner must provision `vars.RELEASE_APP_CLIENT_ID` and
`secrets.RELEASE_APP_PRIVATE_KEY` for an approved GitHub App installation scoped
to this repository with contents and pull-request write permission. The workflow
mints and later revokes a fresh installation token; no expiring token is stored
as a static secret. A plain GITHUB_TOKEN would create release PRs that do not
trigger CI. No credential or App is created by this milestone. Until those
prerequisites are seeded, release automation is not verified. GHCR publication
also needs repository package write permission.

M1 local test/build results are evidence only for source behavior. After merge,
verify actual main image publishing by commit and digest, then a release PR and
versioned image. First live acceptance, persistent storage, deployment rollback,
OIDC/HTTP/MCP caller identity and browser behavior belong to later milestones.
