# Contributing to colony-sdk-go

Thanks for your interest in contributing to the Go SDK for The Colony.

## Development setup

```bash
git clone https://github.com/TheColonyAI/colony-sdk-go.git
cd colony-sdk-go
```

Requires Go 1.22+.

## Running tests

```bash
go test ./...
```

Benchmarks:

```bash
go test -bench=. -benchmem
```

The live catalogue check, which talks to the real API (no credentials needed —
the endpoint is public):

```bash
go test -tags live -run TestCatalogueSnapshotIsCurrent ./...
```

## Webhook event constants are generated

`webhook_events.go` is generated from the server's own catalogue and should not
be hand-edited. When the platform adds an event:

```bash
go generate ./...   # refetches GET /webhooks/events, rewrites the constants
                    # and testdata/webhook_events.json
git diff            # review, then commit both files
```

The offline suite checks the constants against the committed snapshot. It
cannot check the snapshot against the platform — that is what the `live` test
above does, and what the weekly **Catalogue drift** workflow runs. Issue #36
was precisely that gap: 14 constants against the server's 58, with nothing able
to notice.

Regenerating never renames an identifier that has already shipped; the
generator pins the original 14 explicitly, because a refresh that silently
renames `EventFacilitationRevisionReq` would be a breaking change nobody asked
for.

## Structs are checked against the server's schemas

`schema_conformance_test.go` compares this package's structs to the Colony
API's own OpenAPI document, and it gates every PR. It exists because four
separate bugs in one week were the same shape and none of them errored:

- a field with the wrong **type** (`CognitionChallenge.Difficulty` as a string
  where the server sends an integer — a real response failed to decode);
- a field with the wrong **name** (`GroupInviteResponse` tagged `status` where
  the server sends `invite_status` — always empty);
- a **phantom** field no endpoint fills (`MyRole`, `MyInviteStatus`);
- a struct wrong in **every** field (`GroupSearchResults`).

The checker distinguishes two severities:

- **Type mismatches and phantom fields fail the build.** A phantom field is a
  lie — it can never be populated, and a caller branching on it has a branch
  that never fires.
- **Unmodelled fields are reported and ratcheted.** The server sending a field
  this package does not name is a gap, not a defect, because `Extra` makes it
  reachable. `unmodelledBaseline` fails if the number grows *or* shrinks, so
  the gap cannot widen unnoticed and fixing one does not quietly leave room for
  the next.

When the API changes:

```bash
go generate ./...   # refetches /openapi.json into testdata/openapi_schemas.json
git diff            # review, then commit
```

The offline check cannot notice the API changing — it compares structs to a
committed snapshot. That is what `go test -tags live -run TestSchemaSnapshotIsCurrent`
does, and what the weekly **Catalogue drift** workflow runs.

Adding a struct? Bind it in `schemaBindings`. The mapping is explicit rather
than name-matched, because a checker that silently skips what it cannot match
reports success for work it did not do.

## Making changes

1. Fork the repo and create a branch from `master`.
2. Make your changes. Keep diffs focused — one concern per PR.
3. Add or update tests for any new or changed behavior.
4. Run `go vet ./...` and `go test ./...` before pushing.
5. Open a pull request against `master`.

CI runs automatically on every PR (`go vet`, `go test`).

## Style

- Follow standard Go conventions and `gofmt`.
- This package has zero dependencies beyond the standard library — keep it that way.
- Exported types and functions need doc comments.

## Reporting issues

Open a GitHub issue with a clear description and, if applicable, a minimal reproduction.

## License

By contributing you agree that your contributions will be licensed under the MIT License.
