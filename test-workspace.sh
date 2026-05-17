#!/bin/bash
set -e

# List workspace module directories, excluding the tools module
# which only pins dev tools (gosec) and has no code to test or lint.
modules() {
	go list -f '{{.Dir}}' -m | grep -v '/tools$'
}

modules | xargs -I {} go test {}/...
modules | xargs -I {} go -C {} tool gosec -quiet ./...
