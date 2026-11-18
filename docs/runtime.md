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
| `UW_GROUPS` | JSON array of `{Claim, ID, Name}` configured sharing-group mappings |
| `UW_ADMINS` | JSON array of exact `{Issuer, Subject}` administrator identities |
| `UW_ADMIN_GROUPS` | JSON array of verified claim values granting metadata administration |

The callback is the configured origin plus `/auth/callback`. Configure code flow,
S256 PKCE, and the needed group scope on the provider. Only asymmetric RS256,
ES256 and EdDSA tokens are accepted. Issuer/subject are used unchanged by both
transports, never email, display names or identity headers. First verified sign-in
creates only the caller's account. Configured group claims reconcile only that
caller's mapped memberships. Explicitly disabled accounts/groups remain disabled.
Administrator mapping grants metadata administration without content bypass.
Configured sharing group IDs must be positive, unique and stable across restarts;
changes to the mapping require a reviewed metadata migration. Unmapped group
memberships can be managed with the admin operations; claim-mapped memberships
are authoritative from the identity provider and reconcile on each request.

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
configured verified claims into the caller's account metadata; all content reads
and mutations retain the M1 operations. Member DTOs add buyer display name and
surprise creator ID for coordination; owner DTOs remain unchanged. All wire IDs
are decimal strings to preserve 63-bit random item IDs in browsers. The wire
conversion changes representation only and never filters secret fields.

Operations: `lists`, `detail`, `deleted`, `create_list`, `update_list`,
`add_request`, `update_request`, `delete_request`, `restore_request`,
`add_surprise`, `claim`, `release_claim`, `delete_surprise`, `users`, `groups`,
`set_user`, `create_group`, `set_group`, `set_membership`. MCP uses these tool
names; HTTP uses them as path suffixes. Arguments are a JSON object using
`list`, `item`, `user`, `group` string IDs; `name`, `date`; `groups` string IDs;
`request` with M1 `Name/Link/Price/Note/Image/Quantity/Priority` fields;
`archived`, `offset`, `limit`, `disabled`, `active` as relevant. Unknown fields,
including identity arguments, are rejected. Pagination defaults to 100; the UI
uses 20 and provides previous/next controls. Owner mutations return owner types;
coordination mutations return the caller's freshly scoped list view. Metadata
operations use M1's admin authorization plus verified per-request admin mapping.

Archives are calculated in M1 from the configured timezone on each operation.
There is no archive job or extra database path. Archived owner responses still
exclude all purchase/surprise fields, and archived content writes are rejected.

## Verification

`go run ./cmd/check-boundary`, `go vet ./...`, `go test -race -count=1 ./...`,
and the repeated M1 differential/race tests remain required. The guard's import
allowlist now includes the authentication/transport libraries and embedded
assets; only the fixed asset module can anonymously import `embed`. It still
rejects filesystem access, SQL and unreviewed data exports/signatures elsewhere.
The two new data DTOs, configuration fields and `SignIn` signature are explicitly reviewed additions.

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
