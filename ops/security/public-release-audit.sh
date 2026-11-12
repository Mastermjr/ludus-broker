#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"

for command in git gitleaks; do
  command -v "$command" >/dev/null || {
    echo "error: required command not found: $command" >&2
    exit 1
  }
done

for path in .private .pi .beads; do
  if git ls-files --error-unmatch "$path" >/dev/null 2>&1 ||
     git ls-files "$path/**" | grep -q .; then
    echo "error: private path is tracked: $path" >&2
    exit 1
  fi
  git check-ignore -q "$path" || {
    echo "error: private path is not ignored: $path" >&2
    exit 1
  }
done

if git ls-files | grep -E '(^|/)(broker\.db($|-)|proxmox-token\.json$|secrets\.sops\.ya?ml$|discord-token$|ludus-api-key$|ludus-tunnel$|keys\.txt$)' ; then
  echo "error: deployment state or credential-shaped file is tracked" >&2
  exit 1
fi

gitleaks git --redact --no-banner

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# Scan exactly the tracked and non-ignored files that could enter the next
# commit, without traversing ignored private state.
git ls-files -co --exclude-standard -z |
  tar --null --files-from=- --create --file=- |
  tar --extract --file=- --directory="$tmp"
gitleaks dir --redact --no-banner "$tmp"

echo "Public-release secret and private-path audit passed."
