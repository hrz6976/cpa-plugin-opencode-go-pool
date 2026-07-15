# OpenCode Go Pool for CLIProxyAPI

`opencode-go-pool` is a quota-aware [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)
plugin for pooling multiple [OpenCode Go](https://opencode.ai/docs/go/)
subscriptions across OpenAI-compatible and Anthropic-compatible clients.

One OpenCode Go API key represents one logical account. CLIProxyAPI exposes
that account as two credentials—one per protocol—but both credentials share
the same 5-hour, weekly, and monthly quota. This plugin pairs the credentials
automatically and keeps their health state synchronized.

## Features

- **Cross-protocol failover:** a 429, 401, or 403 from either credential
  excludes the whole logical account, including its sibling protocol.
- **Quota-window awareness:** OpenCode Go 429 responses are classified as
  5-hour, weekly, or monthly limits and held until `Retry-After` expires.
- **Proactive quota protection:** optional dashboard polling stops routing to
  accounts at a configurable threshold before they hard-fail.
- **Low-interference scheduling:** while all accounts are healthy, CPA's
  built-in session-affinity round-robin remains in control. The plugin takes
  over only when an account is impaired.
- **Management UI and API:** inspect quota windows, configure dashboard
  access, refresh usage, and manually unblock accounts.
- **Automatic account discovery:** matching API keys in
  `openai-compatibility` and `claude-api-key` are paired without hand-written
  runtime auth IDs.

## Requirements

- CLIProxyAPI v7.2.67 or a compatible release with the C ABI plugin system
- Linux amd64 with glibc 2.36 or newer for the default build target
- Go 1.26 with CGO enabled and a local C compiler toolchain
- `save-cooldown-status: true` in CPA for reliable persisted cooldown signals

## Installation

### CPA Manager Plus plugin store

Add this custom registry to CPA's `config.yaml` and make sure the plugin
directory is persisted across container replacements:

```yaml
plugins:
  enabled: true
  dir: plugins
  store-sources:
    - https://raw.githubusercontent.com/hrz6976/cpa-plugin-opencode-go-pool/main/registry.json
```

Reload or restart CPA, open **CPA Manager Plus → Plugin Store**, refresh the
store, and install **OpenCode Go Pool**. The installer verifies the release
checksum and writes the versioned library under `plugins/linux/amd64/`.

The published package currently supports Linux amd64 with glibc 2.36 or newer.
Installing the plugin does not create OpenCode credentials; complete the
[configuration](#configuration) section below before enabling production
traffic.

### Manual build and installation

Build the shared library:

```sh
make test
make build VERSION=0.1.0
make package VERSION=0.1.0  # produce the CPA plugin-store zip + checksums
```

Copy `dist/opencode-go-pool-v0.1.0.so` into CPA's
`plugins/linux/amd64/` directory and mount that directory at
`/CLIProxyAPI/plugins` in the CPA container. The plugin ID is derived from the
filename by removing the version suffix, so the example above registers as
`opencode-go-pool`.

For a local stack with a container named `cli-proxy-api`, this shortcut builds,
copies, and restarts CPA:

```sh
make deploy DEPLOY_DIR=/path/to/stack/plugins/linux/amd64
```

## Configuration

Start from [`config.example.yaml`](config.example.yaml). The important rule is
that every OpenCode Go API key must appear once in the named
`openai-compatibility` provider and once in `claude-api-key`, using these
upstream base URLs:

| Protocol | Base URL |
| --- | --- |
| OpenAI-compatible | `https://opencode.ai/zen/go/v1` |
| Anthropic-compatible | `https://opencode.ai/zen/go` |

The plugin reproduces CPA's stable auth-ID hash and pairs entries that contain
the same API key. Adding or rotating keys requires CPA to reload the config; a
container restart may be necessary when `config.yaml` is a single-file bind
mount.

Default plugin settings are:

| Setting | Default |
| --- | --- |
| `compat-name` | `opencode-go` |
| `claude-base-url` | `https://opencode.ai/zen/go` |
| `cpa-config-path` | `config.yaml` |
| `threshold-percent` | `97` |
| `sticky-ttl` | `24h` |
| `suspend-duration` | `30m` |
| `fallback-cooldown` | `10m` |
| `dashboard-refresh-interval` | `3m` |
| `dashboard-stale-after` | `20m` |
| `cooldown-dir` | `/root/.cli-proxy-api` |
| `state-dir` | `/root/.cli-proxy-api/opencode-go-pool` |

Dashboard polling is optional. Without it, passive 429/401/403 handling and
cross-protocol health synchronization still work.

## Dashboard credentials

Open the CPA plugin page at:

```text
/v0/resource/plugins/opencode-go-pool/status
```

Each account can be configured with its OpenCode workspace ID and dashboard
`auth` cookie. The cookie is more sensitive than an API key:

- it is saved only in `state-dir/settings.json` with mode `0600`;
- it is never returned by the management API or written to logs;
- it should not be committed to Git or placed directly in `config.yaml`;
- it can instead be supplied through a `cookie-file` mounted read-only into
  the CPA container.

Dashboard data becomes inactive after `dashboard-stale-after`, so an expired
cookie or changed page format falls back safely to passive error handling.

## Management API

All management endpoints require the CPA management key.

| Route | Purpose |
| --- | --- |
| `GET /v0/management/plugins/opencode-go-pool/status` | Return pool state |
| `POST /v0/management/plugins/opencode-go-pool/refresh` | Refresh dashboard usage |
| `POST /v0/management/plugins/opencode-go-pool/unblock` | Clear one account's plugin-level blocks |
| `POST /v0/management/plugins/opencode-go-pool/account-config` | Save or clear dashboard settings |

Example unblock payload:

```json
{"account":"go-1"}
```

Example dashboard configuration payload:

```json
{"account":"go-1","workspace_id":"wrk_REPLACE_ME","cookie":"auth=REPLACE_ME"}
```

To remove saved dashboard credentials, send:

```json
{"account":"go-1","clear":true}
```

## How health synchronization works

The plugin listens for CPA usage failure records and also scans CPA's `.cds`
cooldown files every 10 seconds. The persisted files are important because
CPA v7.2.67 can drop asynchronous success records after request cancellation;
failure records and `.cds` cooldowns still provide the signals needed for
failover.

When every candidate is healthy, the scheduler returns `Handled=false` and
preserves CPA's native affinity. When any logical account is impaired, it
selects only from healthy accounts and maintains header-based sticky bindings
for the configured TTL. If no healthy account remains, control returns to CPA
so the host can produce its normal error/retry behavior.

## Development

The default targets use the local Go and C toolchains:

```sh
make test
make build
make clean
```

`make test` runs `go vet ./...` and `go test ./...`. `make package` creates
`opencode-go-pool_<version>_linux_amd64.zip` and `checksums.txt` in `dist/`
using the exact CPA plugin-store layout. CI additionally checks formatting and
builds the C ABI shared library. Pushing a `v<version>` tag publishes the
installer artifacts as a GitHub Release.

## Known limitations

- OpenCode does not provide a stable official usage API. Dashboard polling
  parses the workspace page and may require updates if its markup changes.
- Dashboard cookies expire. The management page reports refresh errors; stale
  quota data is ignored automatically.
- Success counters in CPA v7.2.67 are best-effort because of the asynchronous
  usage-dispatch behavior. Use CPA Manager Plus for authoritative request and
  token accounting.
- Credential pairing depends on CPA's current stable auth-ID algorithm and
  should be revalidated after major CPA upgrades.

## License

[MIT](LICENSE)
