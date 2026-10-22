# TODOs

## Update the dependents once v1.0.0 is tagged

Go repositories of ungerik and domonda that require `github.com/ungerik/go-fs`
or one of its backend modules, found with
`gh search code "github.com/ungerik/go-fs" --owner ungerik --owner domonda --filename go.mod`
and in the local checkouts (2026-10-22). Update the libraries first, the services
get go-fs through them too. `docs/MIGRATION_v1.md` lists the changes; most of them
fail to compile, so `go build ./...` finds the call sites.

For every go.mod: `go get github.com/ungerik/go-fs@v1.0.0` (plus each backend
module the go.mod requires, all modules are tagged in lockstep), `go mod tidy`,
fix the build, run the tests.

### Libraries

- [ ] [domonda/golog](https://github.com/domonda/golog) `go.mod` (v0.1.0)
- [ ] [domonda/go-structtable](https://github.com/domonda/go-structtable) `go.mod` (v0.1.0)
- [ ] [domonda/go-retable](https://github.com/domonda/go-retable) `go.mod` (pseudo-version 2026-02-19)
- [ ] [domonda/go-rcom](https://github.com/domonda/go-rcom) `go.mod` (v0.1.0)
- [ ] [domonda/go-docdb](https://github.com/domonda/go-docdb) `go.mod` (pseudo-version 2026-06-29)
- [ ] [domonda/go-htmlform](https://github.com/domonda/go-htmlform) `go.mod` (v1.0.0-beta.1)
- [ ] [ungerik/go-dynconfig](https://github.com/ungerik/go-dynconfig) `go.mod`, `loadenv/go.mod`, `example/go.mod` (pseudo-version 2026-06-29)
- [ ] [ungerik/go-mx](https://github.com/ungerik/go-mx) `go.mod`, `mcp/go.mod` (v0.1.0)

### Services and applications

- [ ] [domonda/api](https://github.com/domonda/api) `golang/domonda/go.mod`, `gen-invoice-schema/go.mod` (pseudo-version 2026-02-23)
- [ ] [domonda/domonda-service](https://github.com/domonda/domonda-service) (private) `go.mod` with `ftpfs` and `sftpfs` (v0.1.0), `pkg/deprecated/apis/gini/go.mod` (pseudo-version 2025-06-25)
- [ ] [domonda/idwell-finance-service](https://github.com/domonda/idwell-finance-service) (private) `go.mod` with `ftpfs` and `sftpfs`, `pkg/deprecated/apis/gini/go.mod` (pseudo-versions 2025/2026)
- [ ] [domonda/idwell-code](https://github.com/domonda/idwell-code) (private) `go.mod` with `ftpfs` and `sftpfs` (pseudo-versions 2024)
- [ ] [domonda/services](https://github.com/domonda/services) (private) `go.mod`, `docbuilder/go.mod`, `tesseract/go.mod` (pseudo-versions 2018)
- [ ] [ungerik/gutz](https://github.com/ungerik/gutz) (private) `go.mod` (pseudo-version 2026-05-21)

### Dormant, update only if they are still used

- [ ] [domonda/Domonda](https://github.com/domonda/Domonda) (private, last push 2022-03) `go.mod`
- [ ] [domonda/domonda-email-handler](https://github.com/domonda/domonda-email-handler) (private, last push 2019-11) `go.mod`
- [ ] [domonda/go-genpgfuncs](https://github.com/domonda/go-genpgfuncs) (last push 2020-08) `go.mod`
- [ ] [ungerik/bloggo](https://github.com/ungerik/bloggo) (last push 2022-08) `go.mod`
- [ ] [ungerik/go-command](https://github.com/ungerik/go-command) (last push 2022-09) `go.mod`
- [ ] `domonda/go-function` `htmlform/go.mod` exists only in the local checkout, the remote has no `htmlform` any more (replaced by domonda/go-htmlform?); delete the local copy or update it
