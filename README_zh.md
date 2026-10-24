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

### 使用已发布镜像启动

```bash
# 1. 克隆仓库
git clone https://github.com/Chiiz0/v2raya-plus.git
cd v2raya-plus

# 2. 拉取最新发布镜像并启动服务
docker compose pull
docker compose up -d

# 3. 打开 Web 管理界面
# 浏览器访问: http://localhost:2017
```

镜像同时内置 V2Fly 与 Xray。默认使用 V2Fly；需要让主连接和透明代理使用 Xray（例如 VLESS Vision / REALITY 节点）时：

```bash
V2RAYA_CORE=xray docker compose up -d
```

### Ubuntu 24 / 26 服务器一键安装（Docker + 宿主机透明代理）

如果宿主机本身也需要使用 v2rayA-Plus 的透明代理，同时仍然需要通过项目创建 Docker SOCKS 节点，使用专用安装脚本：

```bash
sudo bash scripts/install-ubuntu-transparent.sh
```

需要主服务使用 Xray 时：

```bash
sudo CORE="xray" bash scripts/install-ubuntu-transparent.sh
```

脚本默认会在 Ubuntu 24.x / 26.x 上安装 Docker / nftables 依赖，构建当前项目镜像，并部署到 `/opt/v2raya-plus`。主服务使用 `network_mode: host`，因此 Web UI 仍监听 `2017`，透明代理规则会写入宿主机网络命名空间。

如果想直接使用已发布的 GHCR 镜像，不在服务器本地构建，可以这样运行：

```bash
sudo BUILD_IMAGE=0 IMAGE=ghcr.io/chiiz0/v2raya-plus:latest bash scripts/install-ubuntu-transparent.sh
```

安装完成后，仍需要在 Web UI 中导入并连接节点，然后开启透明代理。

为避免本项目创建的 Docker 节点流量被宿主机透明代理二次劫持，脚本会创建专用 Docker 网络 `v2raya-plus-proxy`，并让后续创建的 SOCKS 节点容器加入该网络。透明代理启动后，hook 会动态读取这个专用网络的实际 subnet，并在 `inet v2raya` / `tp_rule` 中于 redirect 规则前确保插入源地址直连规则，例如：

```nft
ip saddr <v2raya-plus-proxy subnet> return
```

默认不写死网段，由 Docker 自动分配。如需指定网络名或固定 subnet，可在安装时追加：

```bash
sudo PROXY_NETWORK="<network-name>" PROXY_NETWORK_SUBNET="<cidr>" bash scripts/install-ubuntu-transparent.sh
```

### 停止服务

```bash
docker compose down
```

---

## 🐳 Docker Compose 配置说明

[`docker-compose.yml`](./docker-compose.yml) 使用已发布的 GHCR 镜像启动主服务：

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
   - 按弹窗中选择的内核启动 V2Fly 或 Xray 容器，并绑定到指定端口。
4. SOCKS5 代理立即可用：`localhost:1085`。
5. 在 **Docker 代理**标签页中统一管理所有代理容器；已创建的代理可以修改、暂停、恢复或释放。

Docker 代理引用的节点或所属订阅不能直接删除，前置节点同样受保护；错误会列出相关端口，请先改绑或释放这些代理。删除其他条目或订阅重排时，保存的引用会同步调整，仍对应原节点。刷新订阅时，如果被引用节点消失、配置改变或无法唯一匹配，会拒绝整次更新；并发编辑引起快照冲突时需重新刷新。暂停中的代理也受保护。

> **提示**：可以同时运行 10 个以上节点，各自使用不同端口，互不干扰。

---

## 🔧 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `V2RAYA_CONFIG` | `/etc/v2raya-plus` | v2rayA-Plus 配置和数据库的存储路径 |
| `V2RAYA_CORE` | `v2fly` | 主服务内核：`auto`、`v2fly` 或 `xray`；Vision / REALITY 节点应使用 `xray` |
| `V2RAYA_ADDRESS` | `0.0.0.0:2017` | Web 服务监听地址和端口 |
| `V2RAYA_LOG_LEVEL` | `info` | 日志级别：`trace`、`debug`、`info`、`warn`、`error` |
| `V2RAYA_DOCKER_PROXY_NETWORK` | `v2raya-plus-proxy` | Docker 代理节点容器使用的专用网络 |

---

## 🛠️ 开发与构建

### 离线数据库备份

镜像内提供独立的 `v2raya-db`，支持完整数据库导出、校验和恢复到新文件，保留账号、Docker 引用、出站设置及未知数据。导出前停止主服务，DNS 恢复文件与 Docker 卷分别保存。操作步骤见[数据库备份与恢复](docs/database-backup.md)。

### 可选 SQLite 运行后端

已有安装默认继续使用 Bolt。`v2raya-db runtime-migrate` 可在停主服务后完整迁移并选择 SQLite，支持 WAL、原子事务、备份恢复及保留新写入的 Bolt 回滚。`runtime-export --config-dir` 始终备份当前选中的数据库；切换后不能再把旧 `bolt.db` 当作最新数据。完整步骤和旧版本降级要求见[SQLite 运行后端](docs/sqlite-runtime.md)。

### 构建 Docker 镜像

```bash
docker compose -f docker-compose.dev.yml build v2raya-plus
```

### 强制全量重建（不使用缓存）

```bash
docker compose -f docker-compose.dev.yml build --no-cache v2raya-plus
```

### 项目结构

```
v2rayA-Plus/
├── Dockerfile              # 多阶段构建：node → golang → v2fly-core runner
├── docker-compose.yml      # 使用已发布 GHCR 镜像的编排配置
├── docker-compose.dev.yml  # 本地开发与构建编排配置
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

### DNS 备份与恢复
Redirect 模式劫持 DNS 前，会将 `resolv.conf` 原始内容和权限、符号链接或文件缺失状态保存到持久化配置目录中的 `resolv.conf.v2raya_backup`。备份失败时停止劫持；正常停止、启动失败时恢复原状态，异常退出后会在下次启动时尝试恢复，即使未开启代理自启动。恢复失败会保留备份并记录错误。劫持和恢复过程均不会覆盖符号链接的目标文件。

项目提供的 Compose 和安装脚本均不挂载宿主机 `/etc/resolv.conf`；旧版或自定义部署请在升级前移除此挂载。若旧版已经覆盖原始配置且没有备份，需要通过系统 DNS 管理服务或已知原配置恢复，程序无法重建丢失的内容。重建容器时请保留配置卷，以便读取恢复数据。

开发者可执行 `scripts/test-resolv-docker.sh`，使用临时文件和隔离网络验证文件绑定挂载及新容器中的恢复流程。

### 内核选择与 Docker 代理镜像
主镜像同时包含 V2Fly 和 Xray，通过 `V2RAYA_CORE` 选择主服务内核。每个 Docker 代理还可独立选择“自动 / V2Fly / Xray”：“自动”跟随主服务内核；V2Fly 使用当前运行版本对应的镜像 Tag（如 `v2fly/v2fly-core:v5.41.0`），版本无法获取时降级为 `latest`；Xray 使用 `teddysun/xray:latest`。

使用 [VLESS Encryption](https://xtls.github.io/config/outbounds/vless.html#outboundconfigurationobject) 的节点需要 Xray 25.8.29 或更新版本。URI 中的 `encryption` 参数会在导入、编辑、分享和生成配置时完整保留，未提供时默认为 `none`。创建或替换此类 Docker 代理前，会先使用实际 Xray 镜像验证配置；验证失败时，请执行 `docker pull teddysun/xray:latest` 更新镜像并检查节点参数。升级 v2rayA-Plus 后，需编辑并保存已有 Docker 代理以重新生成配置；旧节点若仍保存原始 URI，可从中恢复加密参数。

Docker 代理页的 **PING** 检查节点 TCP 连接，**HTTP** 则通过每个运行中容器的实际 SOCKS5 代理测试，使用该容器的内核、配置和前置代理链。HTTP 测试会启动临时 `curlimages/curl:latest` 容器，共享代理的网络命名空间；首次测试可能需要下载镜像，也可提前执行 `docker pull curlimages/curl:latest`。测试不会重启代理或主内核，并会分别显示暂停、停止和容器不存在的状态。普通节点列表中的 HTTP 测试仍使用主服务内核。

### XHTTP 高级参数

VLESS 和 VMess 的 XHTTP 节点支持在编辑器中填写高级参数 JSON，包括 `headers`、`xmux`、填充参数和 `downloadSettings` 下载分离。填写 Xray `xhttpSettings.extra` 对象；Host、Path 和 Mode 使用各自的输入框。导入、保存、编辑和分享会保留未知扩展字段，以及显式的 `false`、`0` 和 `-1`；清空高级参数后保存会移除这些配置。

VLESS 分享使用标准的 `mode` 和 URL 编码后的 `extra`，同时兼容旧 `xhttpMode`、`xhttpRawJson` 及上游的展开参数。旧节点若仍保存原始 URI，可从中恢复此前未保存的高级参数。此功能需要 **Xray**；程序会按所用参数检查已知版本要求，创建或替换 Docker 代理前还会验证实际镜像。镜像过旧时先执行 `docker pull teddysun/xray:latest`。已有 Docker 代理需要编辑并保存以重新生成配置。

运行 `scripts/test-xhttp-docker.sh` 可执行隔离的真实 Xray HTTP 回归测试，需要 Docker 和 Go；测试创建临时容器与内部网络，不发布宿主机端口。数据库和新版界面的评估、迁移顺序及验收标准见[上游迁移路线](docs/upstream-migration.md)。

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
- [XTLS/Xray-core](https://github.com/XTLS/Xray-core) — Xray 核心引擎
- [Loyalsoldier/v2ray-rules-dat](https://github.com/Loyalsoldier/v2ray-rules-dat) — GeoSite/GeoIP 规则数据
