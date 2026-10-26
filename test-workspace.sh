#!/bin/bash
set -euo pipefail

# Checks every go.work module like the GitHub Actions workflow does:
#
#   - cross-compiles the root module for the platforms its build tags claim;
#   - go mod tidy -diff, and a build without the workspace (GOWORK=off),
#     so every go.mod and go.sum works on its own like it does for a consumer;
#   - go vet, the tests with the race detector, staticcheck and gosec;
#   - govulncheck.
#
# The tools module only pins the dev tools (gosec, govulncheck, staticcheck)
# and has no code of its own, so only its go.mod and go.sum are checked.
#
# A failing cross-compile target doesn't stop the script: the other targets
# and the module checks still run, and the script fails at the end.
# Any other failure stops the script immediately.

cd "$(dirname "$0")"

# Keep in sync with the cross-compile job in .github/workflows/test.yml
cross_targets="
	freebsd/amd64
	netbsd/amd64
	openbsd/amd64
	dragonfly/amd64
	solaris/amd64
	illumos/amd64
	aix/ppc64
	wasip1/wasm
	js/wasm
	linux/arm64
	windows/arm64
"

# modules prints the directories of the workspace modules except tools
modules() {
	go list -f '{{.Dir}}' -m | grep -v 'tools$'
}

failed_targets=""
for target in $cross_targets; do
	echo "== GOOS=${target%/*} GOARCH=${target#*/} go build ./..."
	if ! GOOS=${target%/*} GOARCH=${target#*/} go build ./...; then
		failed_targets="$failed_targets $target"
	fi
done

echo "== tools"
go -C tools mod tidy -diff

# govulncheck runs on every module without the workspace: in workspace
# mode all modules share one build list, so the newer dependencies of
# one module (the tools module in particular) would hide vulnerable
# versions that a consumer of another module gets.
# "go tool" needs the workspace to find the pinned govulncheck,
# so "go tool -n" builds it and prints the path of the executable.
govulncheck=$(go tool -n govulncheck)

# "go tool" resolves the tools of every workspace module,
# so staticcheck and gosec can be run from any module directory.
for dir in $(modules); do
	echo "== $dir"
	go -C "$dir" mod tidy -diff
	GOWORK=off go -C "$dir" build ./...
	go -C "$dir" vet ./...
	go -C "$dir" test -race -count=1 ./...
	go -C "$dir" tool staticcheck ./...
	go -C "$dir" tool gosec -quiet ./...
	GOWORK=off "$govulncheck" -C "$dir" ./...
done

if [ -n "$failed_targets" ]; then
	echo "FAILED: cross-compiling the root module for:$failed_targets" >&2
	exit 1
fi
echo "All checks passed"
