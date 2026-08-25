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
