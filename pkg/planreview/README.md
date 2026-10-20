# Plan review persistence contract

Go module shared by the ox CLI and every other host that reads or writes plan
review records in a SageOx Ledger. Standard library plus
`golang.org/x/net/html`, which parses a rendered plan page for `Rebind`.
`docs/specs/plan-review-ledger-contract.md` in this repository is the contract
it implements. The initial intended module release is `v0.1.0`, published from
the public ox repository with the tag `pkg/planreview/v0.1.0` before any host
requires it. No module release is performed by these changes.

**Reading.** A host captures a plan's `feedback/` subtree as an `Inventory`: every
file byte-exact, and every entry it could not read as a `Failure`. `Capture`
does this for a local worktree. `Assemble` turns any inventory into the merged
review: the items, every failure and every conflicting operation. A review with
a failure is incomplete and never authoritative. A host proves its transport
(byte-exact inventories, its own failure classes); agreement on items comes from
running `Assemble` over them.

**Writing.** One operation writes one immutable record.
`ParseRoundRequest`/`ParseResolutionRequest` take the exact bytes the client
froze; `Record` adds what the host materializes (slug, acceptance time, the
caller's identity) and names the record by time, operation id and a digest of
its bytes. A round that carries a drawing sends its items under `marks`, and
`ParseRoundRequest` checks each drawn region (`region.go`). Before writing,
`Prior` looks the operation up: the same payload from the same reviewer is
`already_published`, anything else `conflict`. `Publish` writes a record to a
local worktree create-only, without following a symlink at any component.
Authentication, authorization and lifecycle (approval, plan status) stay with
the host. A submission never writes remaps; the plan's author computes them
with `Rebind` and records each with `RemapRecord`.

**Fixtures.** `testdata/review-contract/<fixture>/` holds a plan directory and
what assembling it yields (`expect.json`); `testdata/anchors.json` holds the
review page's anchor, label and markable-element vectors. Record files in the
fixtures never change: hosts compare against them byte for byte.
`testdata/drawn-context/` holds the drawn-region prototype a host qualifies
its viewer with (a document tool, an opaque-origin harness and a local server),
its plan revisions, captures and expected placements, and `regions.json`, the
region rules this module and the browser package both test against; only tests
read it (`docs/specs/plan-review-drawn-context.md`).

Run `go test -race ./...` in this directory or `make test-public-modules` at the
repository root. Root `go test ./...` does not reach this nested module.
