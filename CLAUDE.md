# go-fs

Public, dependency-light Go library. Repo-specific rules that override the global ones:

- **Errors:** use the standard library `errors` / `fmt` packages (`errors.New`,
  `fmt.Errorf` with `%w`). Do NOT add `github.com/domonda/go-errs`; go-fs must not
  pull extra dependencies into every consumer. Sentinel and typed errors live in
  `errors.go`.
- **Roadmap:** `docs/V1_ROADMAP.md` is the plan of record for the v1.0 work. Update it
  when a phase lands or a decision changes.
- **Multi-module workspace:** `go.work` lists the root module and the backend modules
  (`dropboxfs`, `ftpfs`, `s3fs`, `sftpfs`) plus `tools`. Run `./test-workspace.sh` to test
  and lint every module. Keep the `replace github.com/ungerik/go-fs => ..` + placeholder
  pseudo-version pattern in the backend `go.mod` files.
- **Tests:** unit tests must run offline. Tests needing Docker (MinIO, sshd) must
  skip when Docker is unavailable; tests needing credentials or public internet servers
  must be gated behind an environment variable (`GOFS_ONLINE_TESTS=1`,
  `DROPBOX_ACCESS_TOKEN`).
- Prefer `t.Context()` over `context.Background()` in test bodies.
- **Go version policy:** the `go` directive of every module (and `go.work`) is one
  minor version behind the currently released Go (Go 1.27 released → modules on
  `go 1.26.0`). Bump all modules together when a new Go version is released.
