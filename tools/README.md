# Development tools

This module has no code of its own. It exists to pin the versions of the
development tools used by [go-fs](https://github.com/ungerik/go-fs), so that
every contributor and the CI run the same linters and checks:

- [staticcheck](https://staticcheck.dev) (`honnef.co/go/tools/cmd/staticcheck`)
- [gosec](https://github.com/securego/gosec) (`github.com/securego/gosec/v2/cmd/gosec`)
- [govulncheck](https://go.dev/doc/tutorial/govulncheck) (`golang.org/x/vuln/cmd/govulncheck`)

They are declared in a `tool` directive in `go.mod`, with their
dependencies pinned in `go.mod` and `go.sum`. `go tool` builds a tool at
the pinned version on first use and caches the executable in the build
cache, so no separate `go install` step is needed.

## Usage

`go tool` resolves the tools of every module in the `go.work` workspace, so
they can be run from any module directory of the repository:

```bash
go tool staticcheck ./...
go tool gosec -quiet ./...
```

govulncheck has to check a module without the workspace, because in
workspace mode all modules share one build list and the newer dependencies
of one module would hide the vulnerable versions a consumer of another
module gets. `go tool` itself needs the workspace to find the tool, so
build it with `go tool -n`, which prints the path of the executable:

```bash
GOWORK=off "$(go tool -n govulncheck)" ./...
```

`../test-workspace.sh` runs the checks of the GitHub Actions workflow for
every module of the workspace: the cross-compile of the root module,
`go mod tidy -diff`, a build without the workspace, `go vet`, the tests with
the race detector, staticcheck, gosec and govulncheck.

## Upgrading

```bash
go -C tools get -u tool
go -C tools mod tidy
```

Then run `./test-workspace.sh` from the repository root and fix whatever
the new versions report.

This module is not tagged and not released: it is not imported by anything,
and `../tag-release.sh` skips it when it tags the other modules in lockstep.

## License

Part of the [go-fs](https://github.com/ungerik/go-fs) project.
