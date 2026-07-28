#!/usr/bin/env bash
#
# Prints "true" if the diff between two commits contains anything that can
# affect the built binary, "false" if the change is documentation-only.
#
#   needs-build.sh <base-ref> <head-ref>
#
# The verdict goes to stdout; diagnostics go to stderr, so a caller can capture
# one without the other:
#
#   build="$(.github/scripts/needs-build.sh "$BASE_SHA" "$HEAD_SHA")"
#
# It errs toward "true": a missing, unreachable, or undiffable base means a
# build is required rather than silently skipped.
set -euo pipefail

# Paths that cannot change the binary or its test results. Everything else
# builds, so new sources, tooling, and workflow files are covered without
# touching this list — the failure mode is a needless build, not a missed one.
DOC_ONLY=(
  'docs/*'
  '*.md'
  'LICENSE'
  '.github/ISSUE_TEMPLATE/*'
)

base="${1:-}"
head="${2:-HEAD}"

say() { echo "$*" >&2; }
verdict() { say "$2"; echo "$1"; exit 0; }

if [ -z "$base" ] || [ "$base" = "0000000000000000000000000000000000000000" ]; then
  verdict true "No base commit to diff against — building."
fi

# A shallow clone may not have the base commit yet.
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
  git fetch --no-tags --depth=1 origin "$base" 2>/dev/null || true
fi
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
  verdict true "Base commit $base is unreachable — building."
fi

# The base may have moved on since the branch was cut, so diff from the merge
# base to consider only what this branch itself changed.
merge_base="$(git merge-base "$base" "$head" 2>/dev/null || echo "$base")"
files="$(git diff --name-only "$merge_base" "$head")"

if [ -z "$files" ]; then
  verdict false "No files changed."
fi

say "Changed files:"
say "$(echo "$files" | sed 's/^/  /')"

while IFS= read -r file; do
  [ -n "$file" ] || continue
  for pattern in "${DOC_ONLY[@]}"; do
    # shellcheck disable=SC2053 # the unquoted RHS is the glob to match against
    if [[ $file == $pattern ]]; then
      continue 2
    fi
  done
  verdict true "$file requires a build."
done <<< "$files"

verdict false "Documentation-only change."
