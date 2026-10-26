# go-fs

Public, dependency-light Go library. Repo-specific rules that override the global ones:

- **Errors:** use the standard library `errors` / `fmt` packages (`errors.New`,
  `fmt.Errorf` with `%w`). Do NOT add `github.com/domonda/go-errs`; go-fs must not
  pull extra dependencies into every consumer. Sentinel and typed errors live in
  `errors.go`.
- **Multi-module workspace:** `go.work` lists the root module and the backend modules
  (`azureblobfs`, `dropboxfs`, `ftpfs`, `s3fs`, `sftpfs`, `smbfs`, `webdavfs`) plus
  `tools`. Run `./test-workspace.sh` to test
  and lint every module. Keep the `replace github.com/ungerik/go-fs => ..` + placeholder
  pseudo-version pattern in the backend `go.mod` files.
- **Tests:** unit tests must run offline. Tests needing Docker (S3Mock, sshd, Samba, Azurite) must
  skip when Docker is unavailable; tests needing credentials or public internet servers
  must be gated behind an environment variable (`GOFS_ONLINE_TESTS=1`,
  `DROPBOX_ACCESS_TOKEN`).
- Prefer `t.Context()` over `context.Background()` in test bodies.
- **Go version policy:** the `go` directive of every module (and `go.work`) is one
  minor version behind the currently released Go (Go 1.27 released → modules on
  `go 1.26.0`). Bump all modules together when a new Go version is released.

## Skill routing

When the user's request matches an available skill, invoke it via the Skill tool. When in doubt, invoke the skill.

Key routing rules:
- Product ideas/brainstorming → invoke /office-hours
- Strategy/scope → invoke /plan-ceo-review
- Architecture → invoke /plan-eng-review
- Design system/plan review → invoke /design-consultation or /plan-design-review
- Full review pipeline → invoke /autoplan
- Bugs/errors → invoke /investigate
- QA/testing site behavior → invoke /qa or /qa-only
- Code review/diff check → invoke /review
- Visual polish → invoke /design-review
- Ship/deploy/PR → invoke /ship or /land-and-deploy
- Save progress → invoke /context-save
- Resume context → invoke /context-restore
- Author a backlog-ready spec/issue → invoke /spec