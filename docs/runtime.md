# Runtime and authentication

M2 provides an HTTP service with a React interface compiled into the Go binary and
stateless Streamable HTTP MCP at `/mcp`. It has not been deployed. HTTPS termination,
private ingress, OIDC client/resource provisioning, secret mounts and resource
budgets belong to deployment configuration, outside this public repository.

Run `underwraps -database <private-file> -timezone <IANA-zone> -listen :8080`.
Configure the environment privately:

| Variable | Meaning |
| --- | --- |
| `UW_ORIGIN` | Exact HTTPS browser origin without a trailing slash |
| `UW_ISSUER` | Exact HTTPS OIDC issuer; discovery must match it |
| `UW_CLIENT_ID`, `UW_CLIENT_SECRET` | Browser authorization-code client credentials |
| `UW_MCP_AUDIENCE` | Dedicated MCP resource audience, distinct from browser client |
| `UW_GROUPS_CLAIM` | Verified claim containing an array of group strings |
| `UW_SCOPES` | JSON array of additional scopes needed for name/group claims |
| `UW_REQUIRED_GROUP` | Required verified group claim value for sign-in (one string, mandatory) |
| `UW_ADMINS` | JSON array of exact `{Issuer, Subject}` administrator identities |
| `UW_ADMIN_GROUPS` | JSON array of verified claim values granting metadata administration |

The callback is the configured origin plus `/auth/callback`. Configure code flow,
S256 PKCE, and the needed group scope on the provider. Only asymmetric RS256,
ES256 and EdDSA tokens are accepted. Issuer/subject are used unchanged by both
transports, never email, display names or identity headers. First verified sign-in
creates only the caller's account. `UW_REQUIRED_GROUP` must be present in the
verified groups claim on browser sign-in, browser API requests and every MCP
request. An empty setting fails closed. This replaces `UW_GROUPS`, which is no
longer read. `UW_GROUPS_CLAIM` still identifies the claim containing group strings;
`UW_ADMIN_GROUPS` and `UW_ADMINS` still grant metadata administration only.
Existing browser sessions retain their verified claims until expiry; group changes
require a new login or MCP bearer. Explicitly disabled accounts stay disabled.

List visibility is independent of group claims. Owners always see their own
requests; active per-list viewers and managers see the gift coordination view.
Administrators have no content bypass. Users start with `CanShareOwn=false`.
Admin settings select default managers for future lists, and cannot select the
admin making the change or the list owner. Every selected person must first
sign in. Settings do not retroactively alter existing lists. The people picker
returns only IDs and display names, including disabled signed-in accounts;
disabled people cannot be added to sharing or defaults.

Login state, nonce and PKCE verifier remain server-side for five minutes; the
opaque login cookie binds the callback to that browser. Flows are single-use.
Sessions contain identity and verified claims server-side, expire at the earlier
of one hour and the ID token expiry, and disappear on restart. Cookies use Secure,
HttpOnly, SameSite=Lax and the `__Host-` prefix. Successful login rotates the
session; logout invalidates it. There are bounded session/flow maps and expired
entries are pruned on login. Use a single replica. Group changes are seen on a
new login or a new MCP bearer; existing browser sessions retain verified claims
until expiry. Local disabled markers apply on every operation.

Every browser API operation uses `POST /api/<operation>` with JSON and the exact
configured `Origin`. `me` bootstraps the session's CSRF token; all other operations
require `X-CSRF-Token`. A cross-origin request is rejected even before routing.
No permissive CORS, forwarded-identity trust, guest flow or local password exists.
Responses use no-store, no-referrer and a restrictive CSP. Remote images are
stored as manual links only; neither server nor embedded UI fetches product or
image URLs. Links allow only HTTP(S), omit credentials, and open with noopener
and noreferrer. The UI uses React text escaping, never HTML from item fields.

## MCP and the data boundary

The gateway must enforce its own authentication/policy and pass the original
signed person bearer through `Authorization`. This follows the existing people's
backend passthrough contract: the backend independently checks the token's
signature, exact issuer, MCP audience, expiry and nonempty subject. Identity
headers, cookies, browser ID tokens and tool-supplied viewer IDs grant no MCP
identity. Every request, including initialization and discovery, must authenticate.
Stateless per-request MCP servers bind all handlers to that request's verified
principal. There is no session identifier that can reuse another caller's view.
No workload/service-account token mapping is provided.

HTTP and MCP share `operate` and the same `internal/data.Store`. Transport code
cannot query SQLite. `SignIn` is a new viewer-scoped data operation that projects
verified claims into the caller's account metadata; all content reads
and mutations use the same caller-scoped operations. Owner DTOs remain unchanged.
All wire IDs are decimal strings, including `DefaultManagers` and access `UserID`.

Operations: `lists`, `detail`, `deleted`, `create_list`, `update_list`,
`add_request`, `update_request`, `delete_request`, `restore_request`,
`add_surprise`, `claim`, `release_claim`, `delete_surprise`, `users`, `set_user`,
`people`, `sharing`, `set_sharing`. MCP and HTTP use these same names.
Group operations and group arguments have been removed. Unknown fields,
including identity arguments, are rejected. Pagination defaults to 100; the UI
uses 20 and provides previous/next controls. Content mutations return scoped
owner or viewer results; `set_*` operations return an empty object.

- `create_list`: `name`, `date` (YYYY-MM-DD); seeds active managers from the
  owner's default settings, excluding disabled managers.
- `update_list`: `list`, `name`, `date`; changes only owner list metadata.
- `people`: no arguments; display-name picker for signed-in people.
- `sharing`: `list`; returns `Access: [{UserID, Role}]` and `CanEdit`.
- `set_sharing`: `list`, `access: [{user: "ID", role: "viewer"|"manager"}]`;
  atomically replaces active grants. Owner cannot be included. Only a manager
  or an owner with `can_share_own` can edit; archived sharing is frozen. When
  the owner cannot share, at least one enabled manager must remain. An editor
  may remove their own access. Sharing never changes content timestamps/revisions.
- `set_user`: admin only; `user`, `name`, `disabled`, `can_share_own`,
  `default_managers: ["ID"]`. This replaces all settings; omitted booleans are
  false and omitted defaults are empty. Admins cannot name themselves or the
  target user as a default manager. `users` returns these settings with metadata.

Other arguments: `list`, `item`, `user` IDs; `request` with
`Name/Link/Price/Note/Image/Quantity/Priority`; `archived`, `offset`, `limit`.
There is no operation exposing purchase history to list owners or admins.

Archives are calculated in M1 from the configured timezone on each operation.
There is no archive job or extra database path. Archived owner responses still
exclude all purchase/surprise fields, and archived content writes are rejected.

## Verification

`go run ./cmd/check-boundary`, `go vet ./...`, `go test -race -count=1 ./...`,
and the repeated M1 differential/race tests remain required. The guard's import
allowlist now includes the authentication/transport libraries and embedded
assets; only the fixed asset module can anonymously import `embed`. It still
rejects filesystem access, SQL and unreviewed data exports/signatures elsewhere.
Picker/sharing DTOs and the exact updated operations are listed in the boundary allowlist.

Build the UI with `npm ci --prefix web && npm run build --prefix web`. Generated
assets are committed for ordinary `go build` and the existing image pipelines;
CI rebuilds them and rejects a stale bundle. Run browser tests with
`npx --prefix web playwright install --with-deps chromium` followed by
`npm run test:e2e --prefix web`. An optional
`PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` and JSON `PLAYWRIGHT_CHROMIUM_ARGS` allow a
locally installed browser. CI uses Playwright's default browser.

Tests use disposable SQLite files, a synthetic RSA OIDC provider, synthetic
subjects and groups, isolated browser contexts and a fixture-only clock switch.
The fixture route exists only in Go test code when explicitly enabled by the
browser runner. Tests cover invalid state/nonce/PKCE/tokens, sessions/CSRF,
forged callers, complete HTTP/MCP owner responses before/after hidden activity,
and mobile create/prioritize/claim/surprise/delete/restore/archive behavior.
No real identity, household fixture or live provider is needed.
