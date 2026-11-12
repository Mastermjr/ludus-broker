# VibeRange Ludus WireGuard plugin

Ludus normally creates one persistent WireGuard keypair per user. This native
admin plugin adds bounded, authenticated, generation-aware issue, revoke, and
status operations for `user1` through `user5`.

## Build

Go 1.x native plugins require the exact host package hash, not merely source/API
compatibility. Match the deployed server's architecture, exact Go patch version,
workspace-selected module versions, build tags, and every `go:embed` input.
Inspect the installed server with `go version -m`; official 2.3 builds commonly
use `embedwebui,embeddocs`, and those embedded static bytes affect the
`ludusapi` hash even though this plugin does not use them. Build natively on the
Ludus host whenever possible.

Build against the exact deployed Ludus API source:

```bash
ops/ludus-plugin/build-wireguard-plugin.sh \
  /path/to/ludus/ludus-api \
  dist/viberange-wireguard.so
```

The script copies only the plugin sources into a temporary package inside the
Ludus module and removes that package after the build. It is suitable when the
source tree already contains the exact workspace and embedded artifacts used by
the host. A package-hash rejection must fail closed; never replace/rebuild the
Ludus server merely to make a 2.3 plugin load. Ludus 2.4's process-isolated RPC
plugin should replace this native plugin after upgrade.

The exact recovery procedure used for Ludus 2.3.2—including embedded UI/docs
extraction from the running server, native Go 1.26.3 compilation, rollback, and
staging cleanup—is documented in
[`../../docs/ludus-2.3-native-plugin-rebuild.md`](../../docs/ludus-2.3-native-plugin-rebuild.md).

## Install

Copy the `.so` directly to the Ludus host, then run as root:

```bash
ops/ludus-plugin/install-wireguard-plugin.sh \
  /path/to/viberange-wireguard.so \
  /opt/ludus
```

Restart the root-context `ludus-admin` service. Do not enable the production
broker until its authenticated health probe reports the plugin ready.

## Routes

```text
GET  /api/v2/user/viberange-wireguard/health
POST /api/v2/user/viberange-wireguard/issue
POST /api/v2/user/viberange-wireguard/revoke
POST /api/v2/user/viberange-wireguard/cleanup
GET  /api/v2/user/viberange-wireguard/status?userID=user1
```

The regular Ludus service proxies `/api/v2/user/...` requests to the admin
service. Existing broker TLS pinning and API-key authentication therefore remain
unchanged.

The issue response is the only place the generated private profile appears.
Plugin state contains public metadata only and is mode `0700/0600` beneath
`/var/lib/ludus/viberange-wireguard`.
