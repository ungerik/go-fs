# go-fs coding standards

Rules a reviewer applies to every diff. They are judgement calls that no
linter checks; `./test-workspace.sh` covers the mechanical ones (gofmt, go vet,
staticcheck, gosec, govulncheck, `go mod tidy -diff`, cross-compiles).
`AGENTS.md` holds the repo rules every agent needs while writing code.

## Behaviour shared by file systems

- **A rule every file system must follow is a conformance check.** A change
  that adds or changes behaviour of `fs.FileSystem`, one of its optional
  interfaces, or a `File` method built on them adds or changes a check in
  `fstest` (`RunConformance`), so every backend runs it, together with a
  violation in `fstest/conformance_test.go` that proves the check fails.
  A test of one backend alone is right only for behaviour of that backend.
- **Copies and moves are checked against every alias of the source.** A
  change to the same-file, into-itself or copy-then-delete logic of
  `CopyFile`, `CopyRecursive`, `Move` or a backend's native `Move` is covered
  by the alias matrix in `fstest/conformance_alias.go`: a link to the source,
  a link to a parent directory, another hard link, another case or Unicode
  normalization of the name, and the root of a `SubFileSystem`. A new kind of
  alias is a new row there, not a one-off test of the case that was reported.
  The bar is that the data survives: the operation may fail, be a no-op or
  replace, but never truncate the source before it is read or remove the
  only copy.

## Code

- **Inline single-use helpers.** An unexported function or method called from
  one place belongs in its caller: it adds a name, a jump and a doc comment
  without reuse. Exceptions: a helper that exists so tests can pass extra
  arguments (`Foo()` calling `foo(time.Now)`), a recursive function, a function
  passed as a value, an interface method, and a build-tagged variant per GOOS.
  A helper that several callers share has a name that says what it does.
- **Error-returning checks are named `Validate`.** A function that checks
  something and returns an error is `ValidateX`/`validateX`; a bool reporter
  may be `ValidX`/`isValidX`. Existing `check*` helpers stay unless the diff
  touches them anyway.
- **Go strings are UTF-8.** A name read from a format that stores another
  encoding is converted on read, after checking what the standard library
  already does (`archive/zip` only sets `FileHeader.NonUTF8`, see
  `internal/zipname`). Writers reject invalid UTF-8 instead of storing it.

## Tests

- **`t.Context()` in test bodies,** also the `t` of a `t.Run` subtest, and
  contexts derived from it. `context.Background()` only inside `t.Cleanup`
  callbacks, where the test context is already canceled.
- **A regression test fails without the fix.** For a data-loss or security
  fix, run the new test against the old code (`go test -overlay` with the
  old file) and say in the PR that it fails there.

## Documentation

- **One source of truth per behaviour.** The godoc of the function or
  interface method states the contract. `README.md` and the how-tos in
  `docs/` summarize it and link to the godoc, a backend README states only
  how that backend differs. A diff that restates a contract in several places
  instead of linking is a finding, and so is a diff that changes a contract
  without updating the godoc.
- **`docs/MIGRATION_v1.md` is only about v0.1.0 → v1.** It compares v0.1.0
  with v1 in `v0.1 | v1.0` tables, checked against `git show v0.1.0:<path>`.
  Changes between betas or release candidates go to `CHANGELOG.md`.
