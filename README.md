# Under Wraps

Dated, group-shared wish lists with purchases and surprise gifts kept private from
the recipient. Fresh Go implementation, Apache-2.0 licensed.

M2 adds OIDC-only login, dated group-shared lists, a phone-friendly React UI
embedded in the Go binary, and caller-scoped MCP over Streamable HTTP. Purchases
and surprises stay private from the recipient, including after archival. No live
deployment is claimed.

Requires Go as specified in `go.mod`. Reproduce the checks with:

```sh
go run ./cmd/check-boundary
go vet ./...
go test -race -count=1 ./...
go test -count=10 ./internal/data -run 'TestClaimRace|TestOwnerDifferential'
CGO_ENABLED=0 go build ./cmd/underwraps
```

Build the embedded UI with `npm ci --prefix web && npm run build --prefix web`.
Configure OIDC and group mappings privately as described in [runtime setup](docs/runtime.md),
then run the server with a private database path and calendar timezone.

See [the contract](docs/spec.md), [schema and API](docs/data.md), and
[migrations, recovery and release prerequisites](docs/operations.md).
All real identities, groups, hosts, secrets and deployment values belong in private
configuration. Examples use synthetic identities and `example.invalid` domains.
