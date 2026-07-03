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

### Ubuntu 24 / 26 Server Install (Docker + Host Transparent Proxy)

If the host itself needs v2rayA-Plus transparent proxy while this project still creates Docker SOCKS proxy nodes, use the dedicated installer:

```bash
sudo bash scripts/install-ubuntu-transparent.sh
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
   - Starts a `v2fly/v2fly-core` container bound to `<host>:<port>`.
4. The SOCKS5 proxy is now available at `localhost:1085` on the host.
5. Manage all active proxy containers from the **Docker Proxies** tab in the UI; existing proxies can be edited, paused, resumed, or released.

> **Tip**: You can run 10+ nodes simultaneously on different ports with no conflicts.

---

## 🔧 Environment Variables

| Variable | Default | Description |
|---|---|---|
| `V2RAYA_CONFIG` | `/etc/v2raya-plus` | Path where v2rayA-Plus stores its configuration and database |
| `V2RAYA_ADDRESS` | `0.0.0.0:2017` | Address and port the web service listens on |
| `V2RAYA_LOG_LEVEL` | `info` | Log verbosity: `trace`, `debug`, `info`, `warn`, `error` |
| `V2RAYA_DOCKER_PROXY_NETWORK` | `v2raya-plus-proxy` | Dedicated Docker network used by generated proxy node containers |

---

## 🛠️ Development & Build

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
├── gui/                    # Vue.js 2 frontend
│   └── src/
│       ├── locales/        # i18n: en.js, zh.js (+ fa-ir, pt-br, ru)
│       └── node.vue        # Main node management view (incl. Docker Proxies tab)
└── service/                # Go backend
    ├── conf/               # Configuration & version variables
    ├── server/
    │   ├── controller/     # HTTP handlers (incl. dockerProxy.go)
    │   ├── router/         # Gin route registration
    │   └── service/        # Business logic (incl. dockerProxy.go)
    └── db/configure/       # BoltDB persistence (incl. dockerProxy.go)
```

---

## ⚙️ Key Implementation Notes

### IP Forwarding
IP Forward (`/proc/sys/net/ipv4/ip_forward`) is only written to `1` when explicitly enabled in settings. When disabled, the host system value is **never modified**, preventing disruption to Docker's own networking.

### Docker Proxy Image Resolution
When creating a proxy container, the image tag is dynamically resolved from the running v2ray-core version (e.g. `v2fly/v2fly-core:v5.41.0`). If the version cannot be determined, it falls back to `latest`.

### Configuration Volume Isolation
Each Docker Proxy container gets its own named volume (`v2raya-socks-<port>`) with the generated config written via an `alpine` helper container (since `v2fly-core` uses a distroless image without a shell).

---

## 📄 License

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)

This project is licensed under the **GNU Affero General Public License v3.0**, inherited from the original [v2rayA](https://github.com/v2rayA/v2rayA) project.

---

## 🙏 Credits

- [v2rayA](https://github.com/v2rayA/v2rayA) — Original project by mzz2017 and contributors
- [v2fly/v2fly-core](https://github.com/v2fly/v2ray-core) — V2Ray core engine
- [Loyalsoldier/v2ray-rules-dat](https://github.com/Loyalsoldier/v2ray-rules-dat) — GeoSite/GeoIP rule data
