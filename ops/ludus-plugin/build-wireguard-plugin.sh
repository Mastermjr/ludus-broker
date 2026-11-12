#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 <path-to-ludus-api-source> <output.so>" >&2
  exit 2
}

[[ $# -eq 2 ]] || usage
ludus_api=$(realpath "$1")
output=$(realpath -m "$2")
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
source_dir="$repo_root/plugins/ludus-wireguard"

[[ -f "$ludus_api/go.mod" ]] || { echo "missing Ludus API go.mod under $ludus_api" >&2; exit 1; }
grep -qx 'module ludusapi' "$ludus_api/go.mod" || { echo "source is not the Ludus API module" >&2; exit 1; }

build_dir="$ludus_api/.viberange-wireguard-plugin-build"
cleanup() { rm -rf "$build_dir"; }
trap cleanup EXIT
rm -rf "$build_dir"
mkdir -p "$build_dir" "$(dirname "$output")"
cp "$source_dir/plugin.go" "$source_dir/rotation.go" "$build_dir/"

(
  cd "$ludus_api"
  go build -tags ludus_plugin -trimpath -buildmode=plugin -o "$output" ./.viberange-wireguard-plugin-build
)
chmod 0644 "$output"
echo "built $output"
