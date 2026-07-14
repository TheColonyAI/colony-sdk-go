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
