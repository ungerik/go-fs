# Development tools

This module has no code of its own. It exists to pin the versions of the
development tools used by [go-fs](https://github.com/ungerik/go-fs), so that
every contributor and the CI run the same linters:

- [staticcheck](https://staticcheck.dev) (`honnef.co/go/tools/cmd/staticcheck`)
- [gosec](https://github.com/securego/gosec) (`github.com/securego/gosec/v2/cmd/gosec`)

They are declared in a `tool` directive in `go.mod` and installed as a
side effect of the module's dependencies, so no separate `go install` step
is needed.

## Usage

`go tool` resolves the tools of every module in the `go.work` workspace, so
they can be run from any module directory of the repository:

```bash
go tool staticcheck ./...
go tool gosec -quiet ./...
```

`../test-workspace.sh` runs `go vet`, the tests with the race detector, and
both linters for every module of the workspace — the same steps as the
GitHub Actions workflow.

## Upgrading

```bash
go -C tools get -u
go -C tools mod tidy
```

Then run `./test-workspace.sh` from the repository root and fix whatever
the new versions report.

This module is tagged in lockstep with the other modules of the repository,
even though it is not imported by anything.

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
