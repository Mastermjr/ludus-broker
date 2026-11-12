# Rebuilding the Ludus 2.3 native plugin

## Why this procedure exists

Ludus 2.3 loads community admin extensions as in-process Go plugins. Go requires
the plugin and host to have matching package hashes. Matching the Ludus source
tag is insufficient: the architecture, exact Go patch release, workspace module
selection, build tags, and every byte consumed by `go:embed` must also match.

The installed DFW server was built with Go 1.26.3 and the
`embedwebui,embeddocs` tags. Its UI came from a moving/private build input, so a
normal public-source build produced `plugin was built with a different version
of package ludusapi`. The plugin was made compatible by recovering the exact
static assets served by the running binary and rebuilding natively on the
x86_64 Debian host.

This is a Ludus 2.3 compatibility procedure, not a general build recommendation.
Migrate to the process-isolated Ludus 2.4 RPC plugin when available.

## Safety rules

- Never rebuild or replace the Ludus server merely to make a plugin load.
- Never copy server private keys, API keys, database files, or participant
  profiles into the build workspace.
- Build as an unprivileged staging user; use root only for the final atomic
  install and service restart.
- Record and verify the plugin SHA-256 before installation.
- Keep the existing plugin as a rollback copy until health is proven.
- Treat any ABI/package-hash rejection as fail-closed.

## 1. Capture the installed build identity

Run on the Ludus host:

```bash
go version -m /opt/ludus/ludus-server > ludus-server-buildinfo.txt
strings /opt/ludus/ludus-server \
  | grep -E '^go1\.[0-9]+(\.[0-9]+)?$' \
  | sort -u
```

Verify the exact Go patch version, module versions, architecture, and build tags.
For the validated 2.3.2 installation these were Go 1.26.3 on linux/amd64 with
`embedwebui,embeddocs`. Reconstruct the workspace-selected module graph from
those recorded versions; do not allow `go get` or `-mod=mod` to silently choose
newer dependencies.

Obtain the matching toolchain without installing it system-wide:

```bash
version=v0.0.1-go1.26.3.linux-amd64
curl -fsSL --retry 3 \
  -o go-toolchain.zip \
  "https://proxy.golang.org/golang.org/toolchain/@v/${version}.zip"
unzip -tq go-toolchain.zip
unzip -q go-toolchain.zip
mv "golang.org/toolchain@${version}" go-toolchain
./go-toolchain/bin/go version
```

## 2. Recover the embedded path inventory

The embedded file paths are concatenated with other binary strings, so ordinary
`strings | grep '^webUI/'` misses most entries. The validated extraction used:

```bash
perl -0777 -ne '
  $d=$_;
  $d =~ s/(?=(?:webUI|docs)\/)/\n/g;
  for $s (split /\n/, $d) {
    if ($s =~ /^((?:webUI|docs)\/[A-Za-z0-9_@+.,=\/~:-]*?\.(?:woff2|woff|ttf|html|json|jpeg|webp|wasm|css|svg|png|jpg|ico|txt|xml|map|pdf|md|js|gz))/) {
      print qq{$1\n};
    }
  }
' /opt/ludus/ludus-server \
  | sort -u > embedded-file-paths.txt
```

For the validated server this produced exactly 945 paths: 132 under `webUI/`
and 813 under `docs/`. Treat a different count as a different server build;
inspect it rather than copying these expected numbers blindly.

## 3. Recover the exact served bytes

Fetch each path from the running server itself. Use the server's authenticated
local/private transport and exact TLS trust configuration; do not fetch a newer
GUI release from upstream.

The route mapping is:

```text
webUI/<path> -> /ui/<path>
docs/<path>  -> /ludus/<path>
```

Example fetch function, where `LUDUS_URL` points to the protected local endpoint
and `CURL_TLS_ARGS` contains the environment's exact CA or pin options:

```bash
out=$PWD/recovered-embed
mkdir -p "$out"

fetch_one() {
  p=$1
  case "$p" in
    webUI/*) url="$LUDUS_URL/ui/${p#webUI/}" ;;
    docs/*)  url="$LUDUS_URL/ludus/${p#docs/}" ;;
    *) return 2 ;;
  esac
  mkdir -p "$out/$(dirname "$p")"
  # shellcheck disable=SC2086
  curl --fail --silent --show-error --retry 3 \
    $CURL_TLS_ARGS "$url" -o "$out/$p"
}
export LUDUS_URL CURL_TLS_ARGS out
export -f fetch_one
xargs -d '\n' -n1 -P8 bash -c 'fetch_one "$1"' _ \
  < embedded-file-paths.txt
```

Verify every expected file exists and record counts and hashes before building:

```bash
test "$(find "$out" -type f | wc -l)" -eq \
  "$(wc -l < embedded-file-paths.txt)"
find "$out" -type f -print0 | sort -z | xargs -0 sha256sum \
  > recovered-embed.sha256
```

## 4. Build inside the exact Ludus workspace

Place the recovered `webUI/` and `docs/` trees at the paths expected by the
matching `ludusapi` source. Copy only the plugin Go sources into a temporary
package inside that module, then build natively:

```bash
cd ludus-native-workspace/ludus-api
rm -rf webUI docs
cp -a /secure/staging/recovered-embed/webUI ./webUI
cp -a /secure/staging/recovered-embed/docs ./docs

mkdir -p .viberange-wireguard-plugin-build
cp /secure/staging/plugin.go \
   /secure/staging/rotation.go \
   /secure/staging/router_cleanup.go \
   .viberange-wireguard-plugin-build/

GOTOOLCHAIN=local \
CGO_ENABLED=1 \
CC=gcc \
/path/to/go-toolchain/bin/go build \
  -tags ludus_plugin,embedwebui,embeddocs \
  -trimpath \
  -buildmode=plugin \
  -o /secure/staging/viberange-wireguard.so \
  ./.viberange-wireguard-plugin-build
```

Inspect and record the result:

```bash
/path/to/go-toolchain/bin/go version -m \
  /secure/staging/viberange-wireguard.so
file /secure/staging/viberange-wireguard.so
ldd /secure/staging/viberange-wireguard.so
sha256sum /secure/staging/viberange-wireguard.so
```

The validated v1.1.0 artifact produced during this work had SHA-256:

```text
a594601e23735fe8c9a3c8ae2667e2aa58ff84fc4a0aaf98408837ac0c1e0d02
```

That hash is evidence for this exact artifact, not a substitute for rebuilding
and reviewing after source changes.

## 5. Install with automatic rollback

The private live-validation installer used the following sequence:

1. verify the expected artifact SHA-256;
2. verify the host binary reports Go 1.26.3;
3. copy the existing plugin to a rollback path;
4. install the new file root-owned mode 0644;
5. restart `ludus-admin`;
6. require the service to remain active and log the plugin load event;
7. query authenticated plugin health;
8. restore the prior plugin and restart automatically on any failure.

After installation, verify through the broker's pinned Ludus API path:

```text
GET /api/v2/user/viberange-wireguard/health
```

The v1.1.0 response must report `ready: true`, the exact plugin name, and version
`1.1.0`. Then run bounded issue/status/revoke and router-cleanup acceptance on an
approved disposable slot before production use.

## Cleanup

After successful installation and off-host evidence capture, remove unprivileged
staging artifacts, recovered static assets, temporary toolchains, module
workspaces, and plugin copies from the Ludus host. Retain no private profile or
credential material; none is required for this build.
