#!/bin/bash
set -e

# Vet, test (with race detector) and lint every go.work module,
# excluding the tools module which only pins dev tools
# (gosec, staticcheck) and has no code of its own.
#
# "go tool" resolves the tools of every workspace module,
# so staticcheck and gosec can be run from any module directory.
modules() {
	go list -f '{{.Dir}}' -m | grep -v 'tools$'
}

for dir in $(modules); do
	echo "== $dir"
	go -C "$dir" vet ./...
	go -C "$dir" test -race -count=1 ./...
	go -C "$dir" tool staticcheck ./...
	go -C "$dir" tool gosec -quiet ./...
done
