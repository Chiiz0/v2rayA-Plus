# v2rayA-Plus

[**English**](./README.md) &nbsp;&nbsp;&nbsp; [**简体中文**](./README_zh.md)

**v2rayA-Plus** 是 [v2rayA](https://github.com/v2rayA/v2rayA) 的 Docker 专属分支，核心扩展特性：同时连接**多个 V2Ray/Xray 节点**，并将每个节点作为独立的 SOCKS5 代理通过 Docker 容器暴露到宿主机的不同端口。

---

## ✨ 特性

- **多节点 Docker 代理** — 同时运行多个节点代理容器，每个节点独占一个宿主机端口。
- **配置持久化** — 所有配置存储在 Docker Named Volume（`v2raya-plus-data`），容器重启不丢失数据。
- **纯 Docker 架构** — 管理界面与所有代理容器均通过 Docker 编排，无需在宿主机安装任何依赖。
- **Web 管理界面** — 浏览器访问 `http://localhost:2017`。
- **多语言支持** — 界面支持中文、英文，Docker 代理功能全面适配 i18n。
- **协议支持** — 继承 v2rayA 的 VMess、VLESS、Shadowsocks、SSR、Trojan、TUIC 等协议支持。

---

## 🚀 快速开始

### 前置条件

- [Docker Desktop](https://www.docker.com/products/docker-desktop/)（Windows / macOS）或 Docker Engine（Linux）
- `docker compose`（Docker Desktop 已内置）

### 构建并启动

```bash
# 1. 克隆仓库
git clone https://github.com/your-org/v2raya-plus.git
cd v2raya-plus

# 2. 构建并启动服务
docker compose up -d --build

# 3. 打开 Web 管理界面
# 浏览器访问: http://localhost:2017
```

### 停止服务

```bash
docker compose down
```

---

## 🐳 Docker Compose 配置说明

[`docker-compose.yml`](./docker-compose.yml) 启动主服务：

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
      - /var/run/docker.sock:/var/run/docker.sock  # Docker 代理功能所需
      - v2raya-plus-data:/etc/v2raya-plus           # 持久化配置卷
volumes:
  v2raya-plus-data:
```

> **注意**：`/var/run/docker.sock` 挂载是 Docker 代理功能的必要条件，它允许后端在宿主机上创建和管理 SOCKS5 代理容器。

---

## 🔀 Docker 代理（多节点核心特性）

这是 v2rayA-Plus 的核心扩展功能，允许同时运行多个节点代理：

1. 通过 Web 界面**导入**你的节点或订阅。
2. 在任意节点上点击 **Docker** 按钮，指定一个宿主机端口（如 `1085`）。
3. 后端自动执行：
   - 为该节点生成 V2Ray 配置文件。
   - 使用 `alpine` 辅助容器将配置写入 Docker Volume。
   - 启动一个绑定到指定端口的 `v2fly/v2fly-core` 容器。
4. SOCKS5 代理立即可用：`localhost:1085`。
5. 在 **Docker 代理**标签页中统一管理所有代理容器。

> **提示**：可以同时运行 10 个以上节点，各自使用不同端口，互不干扰。

---

## 🔧 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `V2RAYA_CONFIG` | `/etc/v2raya-plus` | v2rayA-Plus 配置和数据库的存储路径 |
| `V2RAYA_ADDRESS` | `0.0.0.0:2017` | Web 服务监听地址和端口 |
| `V2RAYA_LOG_LEVEL` | `info` | 日志级别：`trace`、`debug`、`info`、`warn`、`error` |

---

## 🛠️ 开发与构建

### 构建 Docker 镜像

```bash
docker compose build v2raya-plus
```

### 强制全量重建（不使用缓存）

```bash
docker compose build --no-cache v2raya-plus
```

### 项目结构

```
v2rayA-Plus/
├── Dockerfile              # 多阶段构建：node → golang → v2fly-core runner
├── docker-compose.yml      # 本地开发与测试编排配置
├── gui/                    # Vue.js 2 前端
│   └── src/
│       ├── locales/        # i18n：en.js, zh.js（+ fa-ir, pt-br, ru）
│       └── node.vue        # 节点管理主视图（含 Docker 代理标签页）
└── service/                # Go 后端
    ├── conf/               # 配置项与版本变量
    ├── server/
    │   ├── controller/     # HTTP 请求处理（含 dockerProxy.go）
    │   ├── router/         # Gin 路由注册
    │   └── service/        # 业务逻辑（含 dockerProxy.go）
    └── db/configure/       # BoltDB 持久化（含 dockerProxy.go）
```

---

## ⚙️ 关键实现说明

### IP 转发保护
IP 转发（`/proc/sys/net/ipv4/ip_forward`）仅在设置中**显式开启**时才写入 `1`。关闭时，宿主机的系统值**绝不会被修改**，保护 Docker 自身网络不受干扰。

### Docker 代理镜像版本动态匹配
创建代理容器时，镜像 Tag 从当前运行的 v2ray-core 版本动态解析（如 `v2fly/v2fly-core:v5.41.0`），确保代理容器内核版本与主容器一致。版本无法获取时降级为 `latest`。

### 配置写入方案
由于 `v2fly-core` 使用 Distroless 镜像（无 Shell 环境），通过 `alpine` 轻量辅助容器将节点配置写入各自的独立 Docker Volume（`v2raya-socks-<port>`），然后再挂载给代理容器运行。

---

## 📄 许可证

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL%20v3-blue.svg)](https://www.gnu.org/licenses/agpl-3.0)

本项目基于 **GNU Affero General Public License v3.0** 许可，继承自原始 [v2rayA](https://github.com/v2rayA/v2rayA) 项目。

---

## 🙏 致谢

- [v2rayA](https://github.com/v2rayA/v2rayA) — 原始项目，由 mzz2017 及贡献者开发
- [v2fly/v2fly-core](https://github.com/v2fly/v2ray-core) — V2Ray 核心引擎
- [Loyalsoldier/v2ray-rules-dat](https://github.com/Loyalsoldier/v2ray-rules-dat) — GeoSite/GeoIP 规则数据
