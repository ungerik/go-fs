# go-fs

Public, dependency-light Go library. Repo-specific rules that override the global ones:

- **Errors:** use the standard library `errors` / `fmt` packages (`errors.New`,
  `fmt.Errorf` with `%w`). Do NOT add `github.com/domonda/go-errs`; go-fs must not
  pull extra dependencies into every consumer. Sentinel and typed errors live in
  `errors.go`.
- **Multi-module workspace:** `go.work` lists the root module and the backend modules
  (`azureblobfs`, `dropboxfs`, `ftpfs`, `gitfs`, `onedrivefs`, `s3fs`, `sftpfs`,
  `smbfs`, `webdavfs`), the `cmd/gofs` command line tool and `tools`. Run
  `./test-workspace.sh` to test and lint every module. Keep the
  `replace github.com/ungerik/go-fs => ..` + placeholder
  pseudo-version pattern in the backend `go.mod` files. `cmd/gofs` is the exception: it
  requires released versions of the go-fs modules without `replace`, so that
  `go install github.com/ungerik/go-fs/cmd/gofs@<version>` works. The release PR sets them to
  the version it releases with `./gofs-require.sh` after bumping `VERSION`, and again after any
  later change to a go-fs module: cmd/gofs/go.sum holds the checksums the tags will have, and
  `go.work` replaces that version with the module directories. Untagged versions are resolved
  from the working tree by `./gofs-require.sh --run`, which CI and `./test-workspace.sh` use for
  the `GOWORK=off` commands of cmd/gofs; `tag-release.sh` refuses a stale cmd/gofs/go.sum.
- **Tests:** unit tests must run offline. Tests needing Docker (S3Mock, sshd, Samba, Azurite, git) must
  skip when Docker is unavailable; tests needing credentials or public internet servers
  must be gated behind an environment variable (`GOFS_ONLINE_TESTS=1`,
  `DROPBOX_ACCESS_TOKEN`, `ONEDRIVE_ACCESS_TOKEN`).
- Prefer `t.Context()` over `context.Background()` in test bodies.
- **Go version policy:** the `go` directive of every module (and `go.work`) is one
  minor version behind the currently released Go (Go 1.27 released → modules on
  `go 1.26.0`). Bump all modules together when a new Go version is released.

## Shipping

`/ship` runs all its steps, the reviews, the coverage audit and the documentation audit
included, with two go-fs specifics:

- **Tests:** `./test-workspace.sh` is the only test lane. It runs every check of CI for
  every module. go-fs has no gstack pregate, so the pregate and the ship receipt are left
  out, and their refusals end nothing.
- **PR:** push the feature branch and run `gh pr create --base master`. A release PR is
  titled `vX.Y.Z: <summary>`, the squash subject. It bumps `VERSION`, adds the CHANGELOG
  heading, moves the version pins of the Markdown files
  (`grep -rn '@v<previous>' --include='*.md'`) and runs `./gofs-require.sh` as its last
  edit. After the squash-merge, `./tag-release.sh` tags it from a checkout of master.

## Skill routing

When the user's request matches an available skill, invoke it via the Skill tool. Route only to skills in the session's available-skills list; answer directly for quick questions or small scoped edits.

Key routing rules:
- Product ideas/brainstorming → invoke /office-hours
- Strategy/scope → invoke /plan-ceo-review
- Architecture → invoke /autoplan
- Design system/plan review → invoke /design-consultation or /plan-design-review
- Full review pipeline → invoke /autoplan
- Bugs/errors → fix them directly: one read-only check of the cause, or a focused failing test first when the cause is unclear; then fix it and run that test
- QA/testing site behavior → invoke /qa or /qa-only
- Code review/diff check → invoke /review
- Visual polish → invoke /design-review
- Building UI → once it renders, invoke /design-review --build <preview url> (opt-in)
- Ship/deploy/PR → invoke /ship (merge + deploy: the user types /land-and-deploy)
- Save progress → invoke /context-save
- Resume context → invoke /context-restore
- Author a backlog-ready spec/issue → invoke /spec
