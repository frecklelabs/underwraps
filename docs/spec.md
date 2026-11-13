# v1 contract

The approved product scope is a small family wish-list service. Everyone signs in
through verified OIDC; there are no passwords or guest links. Multiple named lists
have occasion dates and one or more configured sharing groups. Accounts bind to
verified issuer and subject, never email. Administrators manage users and groups
without gaining access to list contents.

Requested items have a name, link, price, note, image, quantity and owner-selected
priority. Authorized viewers other than the owner can mark an item bought, see
who bought it, and add surprise items to avoid duplicate purchases. The owner
never sees claims, buyers or surprise items, including after the occasion.

Deletion is always soft. An owner-deleted request leaves the owner's active list
and appears in their deleted view with its original requested fields. Unbought
deleted requests disappear from other viewers. Bought deleted requests remain
visible to them as bought and removed by owner. Restore returns the original
request and preserves hidden coordination. Both deletion and restore must behave
identically for the owner whether the item was bought or not.

A configured calendar timezone determines the occasion day. After that day lists
are archived and every content mutation is rejected. Records remain available
through the same permission boundary. Hidden activity cannot affect owner-visible
counts, order, timestamps, revisions, errors or mutation results. There are no
notifications or live events in v1.

All persistence is owned by a single internal package. Every content read and
write requires the verified calling identity; handlers, jobs and MCP must use its
viewer-scoped typed operations. Owner types have no claim fields. SQL excludes
surprises before owner count/order/pagination. No external database query service
or raw persistence interface is permitted. CI must guard the full source tree and
exercise negative guard fixtures and a real database secrecy matrix.

M2 implements OIDC, a phone-friendly React UI embedded in Go, and caller-view
MCP behind a gateway. Private single-replica deployment and live acceptance remain
for M3. Hostname and exposure are configuration; first exposure is private.
URL auto-fill and broader exposure are later work. Source and fixture verification do not establish live deployment. Conventional Commits feed release-please; main commits
and releases publish immutable and versioned images respectively.
