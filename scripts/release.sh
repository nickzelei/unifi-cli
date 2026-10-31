#!/bin/sh
# Cut a unifi-cli release by pushing a signed, v-prefixed tag from an up-to-date main.
#
#   scripts/release.sh [-n] [-y] [--skip-ci] VERSION|major|minor|patch
#   mise run release -- [-n] VERSION|major|minor|patch
#
# VERSION is X.Y.Z or X.Y.Z-pre (a leading "v" is accepted and ignored); major, minor and patch bump the
# highest existing vX.Y.Z tag (pre-releases are ignored; with no tags the base is 0.0.0). A hyphenated
# version becomes a GitHub pre-release.
#
# Pushing the tag starts release.yml, which refuses tags GitHub can't verify, runs `mise run check`, builds
# the archives and checksums, attests them, and publishes the GitHub Release. The tag is signed with
# git tag -s, so git signing must be set up (gpg.format and user.signingkey for SSH keys, and the key
# added to GitHub as a signing key).
#
#   -n         dry run: run every check and print what would happen, create and push nothing
#   -y         do not ask for confirmation
#   --skip-ci  do not require green checks on the commit (needs gh otherwise)
set -eu

usage() { sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "release: $*" >&2; exit 1; }

dry=0 yes=0 skip_ci=0 arg=
while [ $# -gt 0 ]; do
  case "$1" in
    -n|--dry-run) dry=1 ;;
    -y|--yes) yes=1 ;;
    --skip-ci) skip_ci=1 ;;
    -h|--help) usage ;;
    -*) echo "release: unknown flag $1" >&2; usage ;;
    *) [ -z "$arg" ] || usage; arg=$1 ;;
  esac
  shift
done
[ -n "$arg" ] || usage

remote=origin branch=main
cd "$(git rev-parse --show-toplevel)"

# Highest vX.Y.Z tag on the remote (pre-releases excluded), without the v; empty when there is none.
latest_release() {
  git ls-remote --tags --refs "$remote" 'v*' | sed 's#.*refs/tags/v##' |
    grep -E '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' |
    sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1 || true
}

semver='^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'
git fetch -q "$remote" "$branch" --tags || die "cannot fetch $remote"
latest=$(latest_release)

case "$arg" in
  major|minor|patch)
    base=${latest:-0.0.0}
    version=$(echo "$base" | awk -F. -v part="$arg" '{
      if (part == "major") print $1+1 ".0.0"; else if (part == "minor") print $1 "." $2+1 ".0"; else print $1 "." $2 "." $3+1 }') ;;
  *)
    version=${arg#v}
    echo "$version" | grep -Eq "$semver" || die "not a version: $arg (want X.Y.Z or X.Y.Z-pre)" ;;
esac
tag=v$version

# A release must sort above the latest release; a pre-release only needs a core version above it.
if [ -n "$latest" ]; then
  core=${version%%-*}
  higher=$(printf '%s\n%s\n' "$latest" "$core" | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)
  if [ "$core" = "$latest" ] || [ "$higher" != "$core" ]; then
    die "$tag is not above the latest release v$latest"
  fi
fi

git ls-remote --exit-code --tags "$remote" "refs/tags/$tag" >/dev/null 2>&1 && die "$tag already exists on $remote"
git rev-parse -q --verify "refs/tags/$tag" >/dev/null && die "$tag already exists locally (git tag -d $tag)"

[ "$(git symbolic-ref --short -q HEAD || true)" = "$branch" ] || die "check out $branch first"
[ -z "$(git status --porcelain)" ] || die "working tree is not clean"
head=$(git rev-parse HEAD)
if [ "$(git config gpg.format || true)" = ssh ] && [ -z "$(git config user.signingkey || true)" ]; then
  die "gpg.format is ssh but user.signingkey is not set; the tag is signed with git tag -s"
fi
[ "$head" = "$(git rev-parse "$remote/$branch")" ] || die "$branch is not at $remote/$branch (git pull --ff-only, or push first)"

if [ "$skip_ci" = 0 ]; then
  command -v gh >/dev/null || die "gh is needed to check CI (or pass --skip-ci)"
  # Check runs on this commit: any failure stops, anything unfinished stops, none at all only warns.
  states=$(gh api "repos/{owner}/{repo}/commits/$head/check-runs?per_page=100" \
    --jq '.check_runs[] | "\(.status) \(.conclusion // "-") \(.name)"') || die "cannot read checks for $head (or pass --skip-ci)"
  if [ -z "$states" ]; then
    echo "release: warning: no checks ran on ${head%"${head#???????}"}; the release workflow still runs the tests" >&2
  else
    bad=$(echo "$states" | grep -Ev '^completed (success|skipped|neutral) ' || true)
    [ -z "$bad" ] || die "checks on $head are not all green:
$bad"
  fi
fi

since=${latest:+v$latest} kind=
case "$version" in *-*) kind=' (pre-release)' ;; esac
echo "Tag:     $tag$kind"
echo "Commit:  $(git log -1 --format='%h %s' "$head")"
echo "Latest:  ${since:-none}"
if [ -n "$since" ]; then
  echo "Changes: $(git rev-list --count "$since..$head") commits since $since"
else
  echo "Changes: first release"
fi

if [ "$dry" = 1 ]; then
  echo "Dry run: would run git tag -s $tag -m $tag $head && git push $remote $tag"
  exit 0
fi
if [ "$yes" = 0 ]; then
  printf 'Create and push %s? The release is published as soon as the workflow passes. [y/N] ' "$tag"
  read -r answer
  case "$answer" in y|Y|yes) ;; *) die "aborted" ;; esac
fi

git tag -s "$tag" -m "$tag" "$head"
git push -q "$remote" "refs/tags/$tag"
url=$(git remote get-url "$remote" | sed -E 's#^git@github.com:#https://github.com/#; s#\.git$##')
echo "Pushed $tag."

# The release workflow refuses tags GitHub can't verify; say so here too, before anyone waits on it.
if command -v gh >/dev/null; then
  sha=$(gh api "repos/{owner}/{repo}/git/ref/tags/$tag" --jq '.object.sha' 2>/dev/null || true)
  verified=$(gh api "repos/{owner}/{repo}/git/tags/$sha" --jq '"\(.verification.verified) \(.verification.reason)"' 2>/dev/null || true)
  case "$verified" in
    "true "*) echo "GitHub verified the tag signature." ;;
    *) echo "release: warning: GitHub did not verify $tag (${verified:-no answer}); the release workflow will refuse it." >&2
       echo "release: add your signing key at https://github.com/settings/keys (type: Signing Key)." >&2 ;;
  esac
fi
echo "release.yml builds and publishes the release:"
echo "  $url/actions"
echo "  $url/releases/tag/$tag"
