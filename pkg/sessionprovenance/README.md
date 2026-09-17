# Native session provenance contract

Dependency-free Go module shared by ox capture/import and the SageOx backend.
The wire version is `1`; the initial intended module release is `v0.1.0`.
Publish from the public ox repository using the tag
`pkg/sessionprovenance/v0.1.0`, before shipping a backend requiring that version.
No module release is performed by these changes.

`Source` describes native byte identity in session metadata. `Record` is the
persistent coverage/exclusion receipt. Offsets always refer to native bytes,
not the redacted export. Both CLI and backend names are aliases of these types;
timestamps use RFC3339 JSON through `time.Time` (including nanoseconds).

`Source.Validate` requires complete provenance for processing; `Record.ValidateSource`
validates byte identity for receipt operations. A receipt with no coverage may
have an empty generation: deletion/recording exclusions can precede capture.
`CheckCoverage` gives exclusions precedence over missing coverage.

Unknown source and receipt fields, including nested coverage, exclusions and
projections, survive JSON round trips. Consumers must mutate decoded structs
rather than reconstructing receipts from known fields. Use `Projections` for
processing receipts; `Extra` retains unknown future properties.

Run `go test -race ./...` in this directory or `make test-sessionprovenance` at
the repository root. Root `go test ./...` does not discover this nested module.
The fixtures cover live capture, historical import and pre-capture exclusion.
