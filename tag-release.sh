#!/bin/bash
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: ./tag-release.sh [--dry-run]
       ./tag-release.sh --check [VERSION]

Releases the version in the VERSION file for every module of go.work:

  1. checks that the branch is master, the working tree is clean,
     HEAD equals origin/master, CHANGELOG.md has a heading for the version
     and none of the release tags exists yet;
  2. creates the annotated tags vX for the root module and <dir>/vX for
     every other module, except the tools module, which only pins
     development tools and is never tagged;
  3. pushes all tags in one atomic push and checks that they all point
     at HEAD on origin;
  4. creates the GitHub release vX with the CHANGELOG.md section of the
     version as notes, as a pre-release if the version has a -suffix;
  5. warms the Go module proxy, so the new versions are available at once.

--dry-run   runs all checks and prints the commands and release notes
            without tagging, pushing or publishing anything.
            It fails if a real run would fail.
--check     checks that the tags of all modules for VERSION (default: the
            VERSION file) exist and point at one commit.
EOF
}

die() {
	echo "error: $*" >&2
	exit 1
}

cd "$(dirname "$0")"

dry_run=false
check=false
version=""
while [ $# -gt 0 ]; do
	case "$1" in
	--dry-run) dry_run=true ;;
	--check) check=true ;;
	-h | --help)
		usage
		exit 0
		;;
	-*)
		usage >&2
		die "unknown option $1"
		;;
	*)
		$check || die "a version argument is only accepted with --check, releases use the VERSION file"
		version=$1
		;;
	esac
	shift
done

if [ -z "$version" ]; then
	[ -f VERSION ] || die "VERSION file not found"
	version=$(tr -d '[:space:]' <VERSION)
fi
semver_re='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
[[ $version =~ $semver_re ]] || die "invalid version \"$version\", expected vMAJOR.MINOR.PATCH with an optional -suffix"

# Collect the module paths and the release tags of all modules except tools.
# The tag prefix of a module is its directory relative to the repository root.
gowork=$(go env GOWORK)
[ -n "$gowork" ] && [ "$gowork" != off ] || die "no go.work workspace found"
root=$(dirname "$gowork")
module_list=$(go list -m -f '{{.Path}} {{.Dir}}')
module_paths=""
tags=""
while read -r path dir; do
	case "$dir" in
	"$root") prefix="" ;;
	"$root/tools") continue ;;
	"$root"/*) prefix="${dir#"$root"/}/" ;;
	*) die "module $path in $dir is not inside the repository $root" ;;
	esac
	module_paths="$module_paths $path"
	tags="$tags $prefix$version"
done <<<"$module_list"

if $check; then
	status=0
	first=""
	for tag in $tags; do
		if ! commit=$(git rev-parse -q --verify "refs/tags/$tag^{commit}"); then
			echo "error: tag $tag does not exist" >&2
			status=1
			continue
		fi
		echo "$tag -> $commit"
		if [ -z "$first" ]; then
			first=$commit
		elif [ "$commit" != "$first" ]; then
			echo "error: tag $tag points at $commit, not at $first like the tags before it" >&2
			status=1
		fi
	done
	if [ $status -eq 0 ]; then
		echo "All module tags of $version point at one commit"
	fi
	exit $status
fi

# problem fails a real run at once; a dry run reports every problem
# and fails at the end.
problems=0
problem() {
	echo "error: $*" >&2
	$dry_run || exit 1
	problems=$((problems + 1))
}

# run prints a command and runs it, unless this is a dry run
run() {
	echo "+ $*"
	$dry_run || "$@"
}

# changelog_section prints the CHANGELOG.md section of the version without
# its heading and the blank lines around it. Headings can be written as
# "## v1.2.3 - 2026-01-02", "## [1.2.3] - 2026-01-02" or "## [v1.2.3]".
changelog_section() {
	local v=${version#v}
	# Bracket expressions instead of backslashes, which awk implementations
	# treat differently in dynamic regular expressions
	HEADING_RE="^## [[]?v?${v//./[.]}[]]?( |\$)" awk '
		found && /^## / { exit }
		found { lines[++n] = $0 }
		!found && $0 ~ ENVIRON["HEADING_RE"] { found = 1 }
		END {
			if (!found) exit 1
			first = 1
			while (first <= n && lines[first] ~ /^[ \t]*$/) first++
			last = n
			while (last >= first && lines[last] ~ /^[ \t]*$/) last--
			for (i = first; i <= last; i++) print lines[i]
		}
	' CHANGELOG.md
}

echo "Releasing $version"

branch=$(git symbolic-ref --quiet --short HEAD || true)
[ "$branch" = master ] || problem "on branch ${branch:-(detached HEAD)}, releases are tagged on master"
[ -z "$(git status --porcelain)" ] || problem "the working tree is not clean"
git fetch --quiet origin master
head=$(git rev-parse HEAD)
[ "$head" = "$(git rev-parse origin/master)" ] || problem "HEAD $head is not origin/master, pull or push first"

if notes=$(changelog_section); then
	[ -n "$notes" ] || problem "the CHANGELOG.md section of $version is empty"
else
	problem "CHANGELOG.md has no heading for $version"
fi

refspecs=""
for tag in $tags; do
	refspecs="$refspecs refs/tags/$tag"
done

# remote_ref prints the object name of ref in the git ls-remote output $1
remote_ref() {
	echo "$1" | awk -v ref="$2" '$2 == ref { print $1 }'
}

# shellcheck disable=SC2086 # word splitting of the refspec list is intended
remote_tags=$(git ls-remote --tags origin $refspecs)
for tag in $tags; do
	if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
		problem "tag $tag already exists locally"
	fi
	if [ -n "$(remote_ref "$remote_tags" "refs/tags/$tag")" ]; then
		problem "tag $tag already exists on origin"
	fi
done

if ! command -v gh >/dev/null; then
	problem "the GitHub CLI gh is not installed"
elif ! gh auth status >/dev/null 2>&1; then
	problem "the GitHub CLI gh is not logged in, run gh auth login"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
notes_file="$tmp/notes.md"
printf '%s\n' "${notes:-}" >"$notes_file"
if $dry_run; then
	echo "Release notes:"
	sed 's/^/    /' "$notes_file"
fi

for tag in $tags; do
	run git tag -a "$tag" -m "$tag"
done
# shellcheck disable=SC2086 # word splitting of the refspec list is intended
run git push --atomic origin $refspecs

if ! $dry_run; then
	# ls-remote with ref patterns doesn't list the peeled refs/tags/<tag>^{}
	# commits, so compare the tag objects on origin with the local ones,
	# which this script just created for HEAD
	# shellcheck disable=SC2086 # word splitting of the refspec list is intended
	remote_tags=$(git ls-remote --tags origin $refspecs)
	for tag in $tags; do
		[ "$(git rev-parse "refs/tags/$tag^{commit}")" = "$head" ] || die "local tag $tag does not point at HEAD $head"
		local_tag=$(git rev-parse "refs/tags/$tag")
		remote_tag=$(remote_ref "$remote_tags" "refs/tags/$tag")
		[ "$remote_tag" = "$local_tag" ] || die "tag $tag on origin is ${remote_tag:-missing}, not the tag object $local_tag for HEAD $head"
	done
	echo "All module tags of $version point at $head on origin"
fi

prerelease=""
case "$version" in
*-*) prerelease=--prerelease ;;
esac
run gh release create "$version" --verify-tag --title "$version" --notes-file "$notes_file" ${prerelease:+"$prerelease"}

# Query every new module version through proxy.golang.org, which makes the
# proxy fetch it from GitHub. Runs in an empty directory, so no go.mod or
# go.work of the repository is involved.
warm_failed=""
for path in $module_paths; do
	echo "+ GOPROXY=https://proxy.golang.org go list -m $path@$version"
	if $dry_run; then
		continue
	fi
	ok=false
	for attempt in 1 2 3; do
		if GOWORK=off GOFLAGS='' GOPRIVATE='' GONOPROXY='' GOPROXY=https://proxy.golang.org go -C "$tmp" list -m "$path@$version"; then
			ok=true
			break
		fi
		if [ $attempt -lt 3 ]; then
			sleep 10
		fi
	done
	$ok || warm_failed="$warm_failed $path@$version"
done
if [ -n "$warm_failed" ]; then
	die "tags and release are published, but the Go proxy did not return:$warm_failed"
fi

if [ $problems -gt 0 ]; then
	die "dry run found $problems problem(s), a real run would fail"
fi
if $dry_run; then
	echo "Dry run finished, nothing was tagged, pushed or published"
else
	echo "Released $version"
fi
