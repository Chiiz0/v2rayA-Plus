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

### Build & Run

```bash
# 1. Clone the repository
git clone https://github.com/your-org/v2raya-plus.git
cd v2raya-plus

# 2. Build and start
docker compose up -d --build

# 3. Open the web UI
# Navigate to: http://localhost:2017
```

### Stop

```bash
docker compose down
```

---

## 🐳 Docker Compose Configuration

The [`docker-compose.yml`](./docker-compose.yml) launches the main service:

```yaml
services:
  v2raya-plus:
    image: v2raya-plus:local
    build: .
    privileged: true
    ports:
      - "2017:2017"
    restart: always
    environment:
      - V2RAYA_CONFIG=/etc/v2raya-plus
    volumes:
      - /lib/modules:/lib/modules:ro
      - /etc/resolv.conf:/etc/resolv.conf
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
5. Manage all active proxy containers from the **Docker Proxies** tab in the UI.

> **Tip**: You can run 10+ nodes simultaneously on different ports with no conflicts.

---

## 🔧 Environment Variables

| Variable | Default | Description |
|---|---|---|
| `V2RAYA_CONFIG` | `/etc/v2raya-plus` | Path where v2rayA-Plus stores its configuration and database |
| `V2RAYA_ADDRESS` | `0.0.0.0:2017` | Address and port the web service listens on |
| `V2RAYA_LOG_LEVEL` | `info` | Log verbosity: `trace`, `debug`, `info`, `warn`, `error` |

---

## 🛠️ Development & Build

### Build the Docker image

```bash
docker compose build v2raya-plus
```

### Rebuild from scratch (no cache)

```bash
docker compose build --no-cache v2raya-plus
```

### Project Structure

```
v2rayA-Plus/
├── Dockerfile              # Multi-stage build: node → golang → v2fly-core runner
├── docker-compose.yml      # Local development orchestration
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
