# Data boundary and schema (schema version 1)

`internal/data` alone imports the SQLite driver and `database/sql`, opens the file,
and runs migrations. `Store.db` is unexported. No operation returns SQL, handles,
rows, connections or callbacks. `Config` requires a private path, a valid IANA
calendar timezone, an injected clock and optional configured administrator
issuer/subject pairs. Identity verification belongs to M2: callers must not
construct identities from unverified request parameters. Display names convey no
permission. The application owns one Store and one mounted database directory.

The source guard parses **every** `.go` file, including tests, nested directories
and excluded build tags. Outside the data package it uses an import allowlist,
rejects drivers and SQL imports (including aliases/dot imports), blocks filesystem
open/read/write references and HTTP file-serving helpers, and rejects unreviewed
data exports and exported SQL-bearing types/signatures. Only the guard's two
source files can read/write files for source inspection/negative fixtures.
Changing this allowlist or persistence package is a boundary change requiring
review; it is not a sandbox against someone editing the guard itself.

| Record | Key and purpose |
| --- | --- |
| users | Integer ID; unique verified issuer + subject; display name, disabled marker |
| groups | Configured group ID/name and disabled marker |
| memberships | Group/user pair and active marker |
| lists | ID, owner, name, occasion date, owner-visible creation/update times and revision |
| shares | List/group pair and active marker |
| items | Random 63-bit ID, list, creator, surprise flag, original requested fields, priority, creation/update times, revision, deleted marker |
| claims | Item key, buyer and active marker; an atomic upsert wins only when unclaimed |
| coordination | Append-only item/actor/action/time history of claim and release transitions |

Foreign keys are enabled. Schema initialization and the version marker are one
transaction. No physical record deletes are used. List archive status is computed
from the injected clock in the configured timezone: the occasion remains writable
through 23:59:59 local time and archives at the following midnight. Moving an
already archived list forward is rejected too.

Every operation below takes `context.Context` and the verified viewer `Identity`.
The only lifecycle operations are `Open(Config)` and `Close()`.

| Operations | Permission and result |
| --- | --- |
| Register | Registers only caller's verified identity; cannot reactivate a disabled user |
| Users, Groups | Configured, active administrators only; user/group metadata, never content |
| SetUser, CreateGroup, SetGroup, SetMembership | Same admin requirement; disabled/active markers retain records |
| CreateList | Caller owns it and must be an active member of every shared group |
| UpdateList | Active-list owner; same sharing checks; replaces shares using active markers |
| Lists | Authorized discovery, active/archive filter, summary counts, stable occasion/ID order, bounded offset/limit |
| ListDetail | Authorized owner or active member of an active shared group; priority descending then item ID ascending |
| DeletedItems | Owner only; original requested-item fields, no coordination projection |
| AddRequest, UpdateRequest, DeleteRequest, RestoreRequest | Active-list owner; bounded to list and requested-item ID; return OwnerItem |
| AddSurprise | Active-list authorized non-owner; returns new hidden ID |
| Claim, ReleaseClaim | Active-list authorized non-owner; self-claim rejected; only buyer can release |
| DeleteSurprise | Active-list creator, still authorized non-owner; soft deletion |

A list owner can access their own list even if their group membership changes.
Other viewers need a current membership and active share/group on every operation.
Admin status grants no content bypass. Unknown, unauthorized, cross-list and
hidden IDs use the same `ErrMissing`. Visibility is checked before page/input
validation. A known authorized archived list returns `ErrArchived` on mutations;
unauthorized list IDs still return `ErrMissing`. Pages require offset >= 0 and
1 <= limit <= 100. Errors expose no buyer data. Request validation requires a
nonblank name, positive quantity and nonnegative priority; prices are display
strings, not currency arithmetic. URLs are stored, never fetched by this package.

`View` contains exactly one of `OwnerView` and `MemberView`; the other JSON field
is omitted. `OwnerItem` has only requested fields and owner-controlled metadata.
Owner SQL has `surprise=0` and the chosen deleted marker in both count and page
queries; it never joins claims. Member queries include active items and bought
owner-deleted requests, with buyer and removed-by-owner metadata. A removed bought
request is presented as a surprise to members while its original requested record
is retained for owner restore. Coordination never updates owner item/list metadata.
Random item IDs avoid leaking hidden inserts through sequence gaps.

Tests use real temporary on-disk SQLite databases. The read matrix covers owner,
members, non-member and non-member admin, each item state, and active/archive
status. Additional tests cover discovery/counts/pages/order, cross-group/list IDs,
membership revocation, admin isolation, archive midnight, buy races across two
Store instances, persistence and migrations. Differential tests clone identical
on-disk starting worlds and compare complete serialized owner responses and
mutation/error results with and without hidden purchases and surprises.
