# v2rayA-Plus

[**English**](./README.md) &nbsp;&nbsp;&nbsp; [**简体中文**](./README_zh.md)

**v2rayA-Plus** is a fork of [v2rayA](https://github.com/v2rayA/v2rayA) redesigned for **pure Docker deployment**. It extends the original with a key feature: the ability to simultaneously connect to **multiple V2Ray/Xray nodes** and expose each as an independent SOCKS5 proxy on a separate host port via Docker containers — making it ideal for scenarios requiring concurrent proxy connections.

---

## ✨ Features

- **Multi-Node Docker Proxies** — Connect to multiple nodes simultaneously, each as an isolated SOCKS5 proxy container on a dedicated host port.
- **Persistent Configuration** — All configuration is stored in a named Docker volume (`v2raya-plus-data`) and survives container restarts.
- **Docker-First Architecture** — The management UI and all proxy containers are orchestrated through Docker. No native OS install required.
- **Web UI** — Clean browser-based interface accessible at `http://localhost:2017`.
- **Multi-language** — UI supports English and Chinese (Simplified), with full i18n coverage for all Docker Proxy features.
- **Protocol Support** — Compatible with VMess, VLESS, Shadowsocks, SSR, Trojan, TUIC, and more (inherited from v2rayA).

---

## 🚀 Quick Start

### Prerequisites

- [Docker Desktop](https://www.docker.com/products/docker-desktop/) (Windows / macOS) or Docker Engine (Linux)
- `docker compose` (bundled with Docker Desktop)

### Run with the Published Image

```bash
# 1. Clone the repository
git clone https://github.com/Chiiz0/v2raya-plus.git
cd v2raya-plus

# 2. Pull the latest published image and start
docker compose pull
docker compose up -d

# 3. Open the web UI
# Navigate to: http://localhost:2017
```

The image bundles both V2Fly and Xray. V2Fly remains the default; to use Xray for the main connection and transparent proxy (for example, VLESS Vision / REALITY nodes), run:

```bash
V2RAYA_CORE=xray docker compose up -d
```

### Interfaces and database backend

The modern Vue 3 interface is the default at `http://localhost:2017/`. The classic interface remains at `http://localhost:2017/legacy/`, with links in navigation and About. Both share accounts, nodes, Docker proxies and settings; switching interfaces does not migrate the database.

Existing deployments continue to use Bolt by default. SQLite can be enabled through the [offline migration, backup and rollback workflow](docs/sqlite-runtime.md), preserving the complete KV data. About displays the actual running backend.

### Image release policy

Ordinary commits, pull requests and manual runs validate the project without creating new Packages. GHCR images are published only after a valid `vMAJOR.MINOR.PATCH` tag (optionally with a prerelease suffix) passes tests. Stable releases update `latest`; prereleases do not overwrite it.

### Ubuntu 24 / 26 Server Install (Docker + Host Transparent Proxy)

If the host itself needs v2rayA-Plus transparent proxy while this project still creates Docker SOCKS proxy nodes, use the dedicated installer:

```bash
sudo bash scripts/install-ubuntu-transparent.sh
```

To use Xray for the main service:

```bash
sudo CORE="xray" bash scripts/install-ubuntu-transparent.sh
```

By default, the script installs Docker / nftables dependencies on Ubuntu 24.x / 26.x, builds the current project image, and deploys it under `/opt/v2raya-plus`. The main service uses `network_mode: host`, so the Web UI still listens on `2017` and transparent proxy rules are written into the host network namespace.

To install with the published GHCR image instead of building locally, run:

```bash
sudo BUILD_IMAGE=0 IMAGE=ghcr.io/chiiz0/v2raya-plus:latest bash scripts/install-ubuntu-transparent.sh
```

After installation, import and connect a node in the Web UI, then enable transparent proxy there.

To prevent Docker proxy node traffic created by this project from being captured again by the host transparent proxy, the installer creates a dedicated Docker network named `v2raya-plus-proxy`, and generated SOCKS proxy containers join that network. After transparent proxy starts, the hook dynamically reads the dedicated network's actual subnet and ensures a source bypass rule is inserted before the redirect rule in `inet v2raya` / `tp_rule`, for example:

```nft
ip saddr <v2raya-plus-proxy subnet> return
```

No subnet is hardcoded by default; Docker assigns one automatically. If you want a custom network name or fixed subnet, pass them during install:

```bash
sudo PROXY_NETWORK="<network-name>" PROXY_NETWORK_SUBNET="<cidr>" bash scripts/install-ubuntu-transparent.sh
```

### Stop

```bash
docker compose down
```

---

## 🐳 Docker Compose Configuration

The [`docker-compose.yml`](./docker-compose.yml) launches the main service with the published GHCR image:

```yaml
services:
  v2raya-plus:
    image: ghcr.io/chiiz0/v2raya-plus:latest
    privileged: true
    ports:
      - "2017:2017"
    restart: always
    environment:
      - V2RAYA_CONFIG=/etc/v2raya-plus
    volumes:
      - /lib/modules:/lib/modules:ro
      - /var/run/docker.sock:/var/run/docker.sock  # Required for Docker Proxies
      - v2raya-plus-data:/etc/v2raya-plus           # Persistent config volume
volumes:
  v2raya-plus-data:
```

> **Note**: The Docker socket mount (`/var/run/docker.sock`) is required to allow the backend to create and manage SOCKS5 proxy containers on the host.

---

## 🔀 Docker Proxies (Multi-Node Feature)

The signature feature of v2rayA-Plus. It allows you to run multiple V2Ray nodes concurrently, each as an isolated SOCKS5 proxy:

1. **Import** your server nodes as usual via the web UI.
2. On any node, click **Docker** to assign it a host port (e.g. `1085`).
3. The backend automatically:
   - Generates a V2Ray config for that node.
   - Writes the config to a Docker volume using an `alpine` helper container.
   - Starts a V2Fly or Xray container, selected in the dialog, bound to `<host>:<port>`.
4. The SOCKS5 proxy is now available at `localhost:1085` on the host.
5. Manage all active proxy containers from the **Docker Proxies** tab in the UI; existing proxies can be edited, paused, resumed, or released.

Deleting nodes or subscriptions used by a Docker proxy is blocked, including front proxy nodes; the error lists the affected ports. Rebind or release those proxies first. Deleting other entries or reordering a subscription keeps stored proxy references on the same nodes. A subscription refresh is rejected if a referenced node disappears, changes configuration, or cannot be uniquely matched; concurrent edits also require retrying the refresh. Paused proxies receive the same protection.

> **Tip**: You can run 10+ nodes simultaneously on different ports with no conflicts.

---

## 🔧 Environment Variables

| Variable | Default | Description |
|---|---|---|
| `V2RAYA_CONFIG` | `/etc/v2raya-plus` | Path where v2rayA-Plus stores its configuration and database |
| `V2RAYA_CORE` | `v2fly` | Main service core: `auto`, `v2fly`, or `xray`; Vision / REALITY nodes should use `xray` |
| `V2RAYA_ADDRESS` | `0.0.0.0:2017` | Address and port the web service listens on |
| `V2RAYA_LOG_LEVEL` | `info` | Log verbosity: `trace`, `debug`, `info`, `warn`, `error` |
| `V2RAYA_DOCKER_PROXY_NETWORK` | `v2raya-plus-proxy` | Dedicated Docker network used by generated proxy node containers |

---

## 🛠️ Development & Build

### Offline database backup

The image includes `v2raya-db` for complete BoltDB export, verification, and restoration to a new file. It preserves accounts, Docker references, outbound settings, and unknown data. Stop the main service before exporting; save DNS recovery data and Docker volumes separately. See [backup and recovery commands](docs/database-backup.md).

### Build the Docker image

```bash
docker compose -f docker-compose.dev.yml build v2raya-plus
```

### Rebuild from scratch (no cache)

```bash
docker compose -f docker-compose.dev.yml build --no-cache v2raya-plus
```

### Project Structure

```
v2rayA-Plus/
├── Dockerfile              # Multi-stage build: node → golang → v2fly-core runner
├── docker-compose.yml      # Published GHCR image orchestration
├── docker-compose.dev.yml  # Local development build orchestration
├── gui-next/               # Default Vue 3 / Vuetify / Pinia / Vite interface
├── gui/                    # Classic Vue.js 2 interface at /legacy/
│   └── src/
│       ├── locales/        # i18n: en.js, zh.js (+ fa-ir, pt-br, ru)
│       └── node.vue        # Main node management view (incl. Docker Proxies tab)
└── service/                # Go backend
    ├── conf/               # Configuration & version variables
    ├── server/
    │   ├── controller/     # HTTP handlers (incl. dockerProxy.go)
    │   ├── router/         # Gin route registration
    │   └── service/        # Business logic (incl. dockerProxy.go)
    └── db/configure/       # Shared Bolt/SQLite configuration (incl. dockerProxy.go)
```

---

## ⚙️ Key Implementation Notes

### IP Forwarding
IP Forward (`/proc/sys/net/ipv4/ip_forward`) is only written to `1` when explicitly enabled in settings. When disabled, the host system value is **never modified**, preventing disruption to Docker's own networking.

### DNS Recovery
Before redirect-mode DNS interception, the original `resolv.conf` content and permissions, symlink, or missing-file state are saved in `resolv.conf.v2raya_backup` inside the persistent configuration directory. Interception stops if the backup cannot be saved. Normal shutdown and failed startup restore the original; after an abnormal exit, the next startup attempts recovery even when the proxy is not set to start automatically. Failed restoration retains the backup and logs the error. Symlink targets are never overwritten by interception or restoration.

The supplied Compose files and installer do not mount the host's `/etc/resolv.conf`. Remove that mount from older or custom deployments before upgrading. If an older version already overwrote the original without a backup, recover it from your system's DNS manager or a known original configuration; the program cannot reconstruct the lost contents. Keep the configuration volume when recreating the container so recovery data remains available.

Developers can run `scripts/test-resolv-docker.sh` to verify file bind mounts and recovery across fresh containers using temporary files and an isolated network.

### Core Selection and Docker Proxy Images
The main image bundles V2Fly and Xray, selected through `V2RAYA_CORE`. Each Docker proxy can independently select Auto, V2Fly, or Xray. Auto follows the main service core. V2Fly uses the image tag matching the running version (for example, `v2fly/v2fly-core:v5.41.0`) and falls back to `latest` when unavailable. Xray uses `teddysun/xray:latest`.

Nodes using [VLESS Encryption](https://xtls.github.io/config/outbounds/vless.html#outboundconfigurationobject) require Xray 25.8.29 or newer. The `encryption` URI parameter is preserved through import, editing, sharing, and configuration generation; omitted values default to `none`. Before creating or replacing an encrypted VLESS Docker proxy, the actual Xray image validates its configuration. If validation fails, update the image with `docker pull teddysun/xray:latest` and check the node parameters. After upgrading v2rayA-Plus, edit and save existing Docker proxies to regenerate their configuration; encryption can be recovered from a node's stored original URI when it is still available.

On the Docker Proxies tab, **PING** checks the node's TCP connection, while **HTTP** checks each running container's actual SOCKS5 proxy, including its selected core, configuration, and front proxy. HTTP tests use temporary `curlimages/curl:latest` containers in the proxy's network namespace; the first test may need to download this image (or run `docker pull curlimages/curl:latest` in advance). Tests do not restart proxies or the main core. Paused, stopped, and missing containers are reported separately. The regular node list's HTTP test continues to use the main service core.

### XHTTP Advanced Settings

The VLESS and VMess node editor accepts an XHTTP advanced JSON object, including `headers`, `xmux`, padding options, and `downloadSettings`. Enter the Xray `xhttpSettings.extra` object; use the separate Host, Path, and Mode fields for base settings. Import, storage, editing, and sharing preserve unknown extensions and explicit `false`, `0`, and `-1` values. Clearing the advanced field and saving removes those settings.

VLESS links use standard `mode` and URL-encoded `extra` parameters, while older `xhttpMode`, `xhttpRawJson`, and upstream flattened fields remain supported. Previously discarded advanced settings can be recovered from a node's stored original URI when available. **Xray is required**; known version requirements are checked for the selected features, and Docker proxies are validated against their actual image before creation or replacement. Update an old image with `docker pull teddysun/xray:latest`. Edit and save existing Docker proxies to regenerate their configuration.

Run `scripts/test-xhttp-docker.sh` for an isolated HTTP integration test with real Xray containers. It requires Docker and Go, creates temporary containers on an internal network, and publishes no host ports. See the [upstream migration roadmap](docs/upstream-migration.md) (Chinese) for the SQLite and UI assessment, implementation sequence, and acceptance criteria.

### Configuration Volume Isolation
Each Docker Proxy container gets its own named volume (`v2raya-socks-<port>`) with the generated config written via an `alpine` helper container (since `v2fly-core` uses a distroless image without a shell).

### Optional SQLite Runtime

Existing installations continue to use Bolt by default. With the main service stopped, `v2raya-db runtime-migrate --config-dir /etc/v2raya-plus` preserves the complete database and selects a separate SQLite runtime with WAL and atomic transactions. `runtime-export` backs up the selected backend; `runtime-restore` installs an archive into a fresh directory; `runtime-rollback` restores the latest SQLite writes into a new Bolt generation. The original Bolt file is retained and must not be mistaken for current data. See the [SQLite runtime guide](docs/sqlite-runtime.md) (Chinese), including backup, interruption recovery, and downgrade steps for older binaries.

---

## 📄 License

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)

This project is licensed under the **GNU Affero General Public License v3.0**, inherited from the original [v2rayA](https://github.com/v2rayA/v2rayA) project.

---

## 🙏 Credits

- [v2rayA](https://github.com/v2rayA/v2rayA) — Original project by mzz2017 and contributors
- [v2fly/v2fly-core](https://github.com/v2fly/v2ray-core) — V2Ray core engine
- [XTLS/Xray-core](https://github.com/XTLS/Xray-core) — Xray core engine
- [Loyalsoldier/v2ray-rules-dat](https://github.com/Loyalsoldier/v2ray-rules-dat) — GeoSite/GeoIP rule data
