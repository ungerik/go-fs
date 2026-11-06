#!/bin/bash
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: ./gofs-require.sh [VERSION]
       ./gofs-require.sh --check [VERSION]
       ./gofs-require.sh --run COMMAND [ARG...]

cmd/gofs requires the go-fs modules by version, without replace directives,
so that "go install github.com/ungerik/go-fs/cmd/gofs@<version>" works.
The release PR lets cmd/gofs require the version it releases, before the
tags of that version exist.

VERSION     sets the go-fs requirements of cmd/gofs/go.mod to VERSION
            (default: the VERSION file), updates cmd/gofs/go.sum and replaces
            the go-fs modules at VERSION with their directories in go.work,
            so the workspace doesn't need the tags.
--check     checks that cmd/gofs requires VERSION (default: the VERSION
            file) of the go-fs modules, that go.work replaces them and that
            cmd/gofs/go.mod and go.sum are tidy for the working tree.
--run       runs COMMAND, resolving the go-fs versions that cmd/gofs/go.mod
            requires from the working tree if they are not tagged on origin
            yet. Use it for the go commands of cmd/gofs without the workspace
            (GOWORK=off), like go mod tidy, go build and govulncheck.

A module zip excludes the nested modules, so cmd/gofs is no part of the
go-fs modules and their checksums are known before they are tagged.
For untagged versions, the go command gets them like it gets the real tags:
from a temporary git repository with local tags for a commit of the working
tree, without the module proxy and checksum database, into a temporary
module cache that the fake versions never leave. The other modules come from
the module cache or GOPROXY like always. The commit has the tracked files
like "git commit -a": "git add" new files first.

Changing a go-fs module after VERSION was set changes its checksum:
run ./gofs-require.sh again, the CI checks that cmd/gofs/go.sum is current.
EOF
}

die() {
	echo "error: $*" >&2
	exit 1
}

root=$(cd "$(dirname "$0")" && pwd)
module_root=github.com/ungerik/go-fs
gomod=$root/cmd/gofs/go.mod
gowork=$root/go.work

# requirements prints "path version" for every go-fs module cmd/gofs/go.mod requires
requirements() {
	awk -v root="$module_root" '
		$1 == "require" { $1 = ""; $0 = $0 }
		($1 == root || index($1, root "/") == 1) && $2 != "=>" { print $1, $2 }
	' "$gomod"
}

# workspace_replacements prints "path@version" for every version specific
# replacement of a go-fs module in go.work
workspace_replacements() {
	awk -v root="$module_root" '
		$1 == "replace" { $1 = ""; $0 = $0 }
		($1 == root || index($1, root "/") == 1) && $3 == "=>" { print $1 "@" $2 }
	' "$gowork"
}

# tag_of prints the release tag of the module path $1 for the version $2
tag_of() {
	local rel=${1#"$module_root"}
	rel=${rel#/}
	echo "${rel:+$rel/}$2"
}

# resolve_untagged exports the environment that resolves the go-fs versions
# cmd/gofs/go.mod requires from the working tree, if they are not tagged on
# origin. Removes the temporary files when the script exits.
resolve_untagged() {
	local reqs versions version path tags="" refspecs="" remote_tags tag missing=0 present=0
	reqs=$(requirements)
	[ -n "$reqs" ] || die "$gomod requires no go-fs module"
	versions=$(echo "$reqs" | awk '{ print $2 }' | sort -u)
	[ "$(echo "$versions" | wc -l)" -eq 1 ] || die "$gomod requires different go-fs versions:" $versions
	version=$versions
	while read -r path _; do
		tag=$(tag_of "$path" "$version")
		tags="$tags $tag"
		refspecs="$refspecs refs/tags/$tag"
	done <<<"$reqs"

	# shellcheck disable=SC2086 # word splitting of the refspec list is intended
	remote_tags=$(git -C "$root" ls-remote --tags origin $refspecs) || die "can't list the tags on origin"
	for tag in $tags; do
		if echo "$remote_tags" | awk -v ref="refs/tags/$tag" '$2 == ref { found = 1 } END { exit !found }'; then
			present=$((present + 1))
		else
			missing=$((missing + 1))
		fi
	done
	[ $missing -gt 0 ] || return 0
	[ $present -eq 0 ] || die "cmd/gofs requires go-fs $version, of which $present module(s) are tagged on origin and $missing are not"
	echo "cmd/gofs requires go-fs $version, which is not tagged yet: resolving it from the working tree" >&2

	tmp=$(mktemp -d)
	# The go command creates the module cache read-only
	trap 'chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT

	# A commit of the files of the index with their content in the working
	# tree, what the release commit will contain. Untracked files are left
	# out like by "git commit -a". A copy of the index leaves the index of
	# the repository untouched.
	local tree commit untracked
	untracked=$(git -C "$root" ls-files --others --exclude-standard -- . ':!cmd/gofs')
	if [ -n "$untracked" ]; then
		echo "warning: the checksums leave out the untracked files, \"git add\" the ones the release commits:" >&2
		echo "$untracked" | sed 's/^/    /' >&2
	fi
	cp "$(git -C "$root" rev-parse --path-format=absolute --git-path index)" "$tmp/index"
	tree=$(GIT_INDEX_FILE="$tmp/index" git -C "$root" add -u &&
		GIT_INDEX_FILE="$tmp/index" git -C "$root" write-tree)
	commit=$(GIT_AUTHOR_NAME=gofs-require GIT_AUTHOR_EMAIL=gofs-require@localhost \
		GIT_COMMITTER_NAME=gofs-require GIT_COMMITTER_EMAIL=gofs-require@localhost \
		git -C "$root" commit-tree -m "working tree" "$tree")
	git init -q --bare "$tmp/repo.git"
	refspecs=""
	for tag in $tags; do
		refspecs="$refspecs $commit:refs/tags/$tag"
	done
	# shellcheck disable=SC2086 # word splitting of the refspec list is intended
	git -C "$root" push -q --no-verify "$tmp/repo.git" $refspecs

	# The go command clones https://github.com/ungerik/go-fs for the go-fs
	# modules, which git rewrites to the temporary repository
	local n=${GIT_CONFIG_COUNT:-0}
	export "GIT_CONFIG_KEY_$n=url.$tmp/repo.git.insteadOf" "GIT_CONFIG_VALUE_$n=https://$module_root"
	n=$((n + 1))
	export "GIT_CONFIG_KEY_$n=protocol.file.allow" "GIT_CONFIG_VALUE_$n=always"
	export GIT_CONFIG_COUNT=$((n + 1))

	# The go-fs modules bypass the module proxy, so it never caches them as
	# not found, and the checksum database, which doesn't know them yet
	local noproxy nosumdb
	noproxy=$(go env GONOPROXY)
	nosumdb=$(go env GONOSUMDB)
	export GONOPROXY="$module_root${noproxy:+,$noproxy}"
	export GONOSUMDB="$module_root${nosumdb:+,$nosumdb}"
	# The other modules come from the module cache before GOPROXY
	GOPROXY="file://$(go env GOMODCACHE)/cache/download,$(go env GOPROXY)"
	export GOPROXY
	export GOMODCACHE="$tmp/modcache"
}

# version_arg prints the version argument $1, or the VERSION file without it
version_arg() {
	local version
	if [ $# -gt 0 ]; then
		version=$1
	else
		[ -f "$root/VERSION" ] || die "VERSION file not found"
		version=$(tr -d '[:space:]' <"$root/VERSION")
	fi
	local semver_re='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
	[[ $version =~ $semver_re ]] || die "invalid version \"$version\", expected vMAJOR.MINOR.PATCH with an optional -suffix"
	echo "$version"
}

case "${1:-}" in
-h | --help)
	usage
	exit 0
	;;
--run)
	shift
	[ $# -gt 0 ] || die "--run needs a command"
	resolve_untagged
	"$@"
	;;
--check)
	shift
	[ $# -le 1 ] || die "too many arguments"
	version=$(version_arg "$@")
	reqs=$(requirements)
	[ -n "$reqs" ] || die "$gomod requires no go-fs module"
	wrong=$(echo "$reqs" | awk -v v="$version" '$2 != v { print $1 "@" $2 }')
	[ -z "$wrong" ] || die "cmd/gofs requires" $wrong "instead of $version, run ./gofs-require.sh $version"
	replacements=" $(workspace_replacements | tr '\n' ' ')"
	while read -r path _; do
		case "$replacements" in
		*" $path@$version "*) ;;
		*) die "go.work doesn't replace $path@$version, run ./gofs-require.sh $version" ;;
		esac
	done <<<"$reqs"
	resolve_untagged
	GOWORK=off go -C "$root/cmd/gofs" mod tidy -diff ||
		die "cmd/gofs/go.mod or go.sum doesn't match the go-fs modules, run ./gofs-require.sh $version"
	echo "cmd/gofs requires go-fs $version, its go.sum matches"
	;;
-*)
	usage >&2
	die "unknown option $1"
	;;
*)
	[ $# -le 1 ] || die "too many arguments"
	version=$(version_arg "$@")

	edits=""
	while read -r path _; do
		edits="$edits -require=$path@$version"
	done <<<"$(requirements)"
	[ -n "$edits" ] || die "$gomod requires no go-fs module"
	# shellcheck disable=SC2086 # word splitting of the edit flags is intended
	go mod edit $edits "$gomod"

	# The workspace replaces the go-fs versions cmd/gofs requires with their
	# directories: the go command reads the go.mod files of the required
	# versions of workspace modules too, which don't exist before the tags.
	# The modules of this repository have the directories of their paths.
	edits=""
	for path_version in $(workspace_replacements); do
		edits="$edits -dropreplace=$path_version"
	done
	while read -r path _; do
		dir=${path#"$module_root"}
		edits="$edits -replace=$path@$version=.$dir"
	done <<<"$(requirements)"
	# shellcheck disable=SC2086 # word splitting of the edit flags is intended
	go work edit $edits "$gowork"

	# The checksums of the go-fs modules change with every change of them
	# before the tags, and go mod tidy refuses to replace a checksum
	gosum=$root/cmd/gofs/go.sum
	if [ -f "$gosum" ]; then
		awk -v root="$module_root" '!($1 == root || index($1, root "/") == 1)' "$gosum" >"$gosum.tmp"
		mv "$gosum.tmp" "$gosum"
	fi

	resolve_untagged
	GOWORK=off go -C "$root/cmd/gofs" mod tidy
	echo "cmd/gofs requires go-fs $version"
	;;
esac
