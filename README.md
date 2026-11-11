# Under Wraps

Dated, group-shared wish lists with purchases and surprise gifts kept private from
the recipient. Fresh Go implementation, Apache-2.0 licensed.

This is the M1 database and release foundation. There is no usable UI, login,
HTTP/MCP listener or deployment yet. The command initializes/migrates a database
and exits. It deliberately cannot expose a database query endpoint.

Requires Go as specified in `go.mod`. Reproduce the checks with:

```sh
go run ./cmd/check-boundary
go vet ./...
go test -race -count=1 ./...
go test -count=10 ./internal/data -run 'TestClaimRace|TestOwnerDifferential'
CGO_ENABLED=0 go build ./cmd/underwraps
```

Initialize a development database (not a server):

```sh
go run ./cmd/underwraps -database ./state/app.sqlite -timezone America/Chicago
```

See [the contract](docs/spec.md), [schema and API](docs/data.md), and
[migrations, recovery and release prerequisites](docs/operations.md).
All real identities, groups, hosts, secrets and deployment values belong in private
configuration. Examples use synthetic identities and `example.invalid` domains.
