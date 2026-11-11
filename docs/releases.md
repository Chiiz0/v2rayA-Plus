# v2.6.1：Vision 透明代理兼容修复（2026-11-11）

镜像：`ghcr.io/chiiz0/v2raya-plus:2.6.1`，目标平台为 `linux/amd64` 和 `linux/arm64`。稳定发布同时更新 `2.6`、提交号和 `latest` 标签；`main` 镜像标签不再更新。发布状态以 [v2.6.1 Release](https://github.com/Chiiz0/v2rayA-Plus/releases/tag/v2.6.1) 和该标签的 Actions 结果为准。

## 修复内容

- 主镜像、两份 Compose 示例及 Ubuntu 安装脚本默认改为 `V2RAYA_CORE=auto`，优先使用内置 Xray。显式内核或二进制设置仍优先；Vision 节点遇到非 Xray 核心时，在主连接、HTTP 测速、Docker 目标/前置代理生成阶段返回明确错误。不会静默删除节点的 flow。
- 存在 Vision 出站时，已开启嗅探的透明入站自动使用 `routeOnly: true`，覆盖 redirect、TPROXY、TUN 和 IPv6 副本。普通节点保留原设置，关闭嗅探会真正关闭；过滤空白排除域名，DNS/API 入站不再被全局 HTTP/TLS/QUIC 嗅探覆盖。
- 默认 `info` 级别保留核心错误日志；核心 warning/error 传递给主服务时保留严重级别。Docker Vision 配置保留错误输出，并在创建或替换代理前由实际 Xray 镜像验证。
- 两套界面增加 Vision 核心要求及透明代理 RouteOnly 提示。

本版同时包含 v2.6.0 之后已合入主分支的变更：legacy iptables 缺少 mangle 表时回退到 nft；REALITY ML-DSA-65（`pqv`）在导入、编辑、分享、存储与 Docker 目标/前置代理间保留并校验；选定的前端依赖更新。完整范围见 [v2.6.0...v2.6.1](https://github.com/Chiiz0/v2rayA-Plus/compare/v2.6.0...v2.6.1)。

V2Fly 5.41.0 [未编码 Vision flow，也未实现相应数据帧](https://github.com/v2fly/v2ray-core/blob/v5.41.0/proxy/vless/encoding/addons.go)。因此仅改 RouteOnly 不足以修复。Xray [会缓存并回放嗅探数据](https://github.com/XTLS/Xray-core/blob/main/app/dispatcher/default.go)，不能把 EOF 简单归因为嗅探吃掉了 ClientHello。

已加入 [真实核心回归脚本](../scripts/test-vision-transparent.sh)：使用临时证书、本地 HTTPS/Vision 服务端和容器内 nft redirect，不接触宿主防火墙或生产配置。Xray 的 RouteOnly、关闭嗅探、改写目标三个场景均应返回 HTTP 200；V2Fly 5.41.0 的两组旧配置对照均应复现 EOF。另有透明入站、IPv6、FakeDNS 配置保留、核心选择、测速、日志和 Docker 目标/前置代理的单元测试。

### 发布产物核验

发布工作流已通过全部检查，包括新版界面 247 项测试、Go/SQLite 服务测试、race、隔离持久化/回滚和双架构数据库测试。镜像索引摘要为 `sha256:3b379ab3fca07527c4f995e82c470541d941335bcacf25acd984cca73ca8e53e`；两个架构的 revision 均为 `6fa63c6c995adaf3ca9a621efc2c8fd11d7b4875`，`2.6`、`6fa63c6`、`latest` 标签指向同一索引。

已在实际 amd64 发布镜像（Xray 26.11.9、V2Fly 5.41.0）通过上述 5 组 TLS 对照，并用临时 Bolt/SQLite 数据完成 v2.6.0 → v2.6.1 → v2.6.0 演练，验证账户登录、节点、出站、端口及两套界面。新版 Xray 默认使用内置 CA，回归脚本已补充显式信任临时测试证书；运行该镜像的回归时请使用[主分支脚本](https://github.com/Chiiz0/v2rayA-Plus/blob/main/scripts/test-vision-transparent.sh)。这项后续修正只涉及测试证书，不改变发布程序、镜像摘要或版本标签。

## 旧部署升级到 v2.6.1

以下步骤针对已经运行的 v2.6.0 Docker Compose 部署。更早版本先参照[完整备份文档](database-backup.md)核实工具支持。使用**实际部署目录、Compose 文件和原项目名**，不要用仓库示例覆盖现有文件。保留数据卷、`network_mode: host`、端口、Docker socket、透明代理 hook、代理网络以及数据库后端设置；本次升级不要求切换 Bolt/SQLite。

Ubuntu 安装脚本的默认部署目录为 `/opt/v2raya-plus`，项目名为 `v2raya-plus`。若原安装使用该脚本，可先运行 `cd /opt/v2raya-plus` 和 `export COMPOSE_PROJECT_NAME=v2raya-plus`；自定义部署应沿用自己的 `-p` / `-f` 参数。项目名变化可能创建新的空数据卷。

### 1. 拉取镜像并备份原部署

先拉取，避免停服后等待下载。以下备份目录包含私密数据，请保留其私有权限。

```bash
docker pull ghcr.io/chiiz0/v2raya-plus:2.6.1
umask 077
upgrade_backup="$PWD/backups/pre-2.6.1-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$upgrade_backup"
# 文件名不同或使用多个 -f 时，备份实际使用的每份 Compose 文件。
cp docker-compose.yml "$upgrade_backup/"
if [ -f .env ]; then cp .env "$upgrade_backup/"; fi
if [ -d hooks ]; then cp -a hooks "$upgrade_backup/"; fi
old_container="$(docker compose ps -q v2raya-plus)"
docker inspect --format '{{.Image}}' "$old_container" > "$upgrade_backup/image-id.txt"
docker inspect --format '{{.Config.Image}}' "$old_container" > "$upgrade_backup/image-ref.txt"

docker compose stop v2raya-plus
docker compose run --rm --no-deps --entrypoint v2raya-db \
  -v "$upgrade_backup:/backup" v2raya-plus \
  runtime-export --config-dir /etc/v2raya-plus --out /backup/runtime.json
docker compose run --rm --no-deps --entrypoint v2raya-db \
  -v "$upgrade_backup:/backup" v2raya-plus verify --in /backup/runtime.json
```

确认备份和校验均成功后再继续；失败时先处理问题，或用 `docker compose start v2raya-plus` 恢复原服务。`runtime-export` 读取当前选中的 Bolt/SQLite，不能用单独复制旧 `bolt.db` 或 SQLite 主文件替代。DNS 恢复记录、Docker 代理配置卷及其他自定义外部文件按[完整备份文档](database-backup.md)单独保存。保留原镜像以便回滚。

### 2. 持久修改实际 Compose 中的镜像和核心

仅修改对应字段，合并进原来的 environment 列表或映射：

```yaml
services:
  v2raya-plus:
    image: ghcr.io/chiiz0/v2raya-plus:2.6.1
    environment:
      V2RAYA_CORE: xray
```

这里推荐旧 Vision 部署明确选择 `xray`；新版本的 `auto` 也会优先选择内置 Xray。旧 Compose/安装脚本可能已经写死 `V2RAYA_CORE: v2fly`，因此只 `pull` 或在 shell 临时设置 `V2RAYA_CORE=xray` 不一定生效：只有 Compose 使用 `${V2RAYA_CORE}` 插值时，shell 变量才会替换它。修改 `.env` 时也应确认 Compose 确实引用了该变量。

如果设置了 `V2RAYA_V2RAY_BIN=/usr/bin/v2ray`，应删除这项旧覆盖或改为 `/usr/bin/xray`；它的优先级高于 `V2RAYA_CORE`。同样检查 `command:` 中的 `--core` / `--v2ray-bin`。保留节点原有 `flow=xtls-rprx-vision`，不要删 flow 伪装成普通 VLESS。

### 3. 重建主服务并验证

```bash
docker compose config --quiet
docker compose up -d --no-deps --force-recreate v2raya-plus
docker compose exec -T v2raya-plus v2raya-plus --version
docker compose logs --since=5m --tail=200 v2raya-plus
```

`docker compose restart` 不会应用新镜像和环境变量。确认程序版本为 `2.6.1`、界面/启动日志显示实际核心为 Xray；仅执行 `xray version` 只能证明二进制存在。保持原透明代理模式，连接 Vision 节点；已开启嗅探的透明入站会自动得到 RouteOnly，无需手工修改生成的 JSON。

在实际透明代理覆盖的主机上验证直连站点及代理站点，例如：

```bash
curl --noproxy '*' --connect-timeout 10 --max-time 30 -I https://www.baidu.com
curl --noproxy '*' --connect-timeout 10 --max-time 30 -I https://github.com
```

检查 TLS 能完成、没有 EOF，并结合核心路由日志确认代理目标确实走指定出站。站点的具体 HTTP 状态以其响应为准。自定义 `V2RAYA_LOG_LEVEL=warn/error` 仍按严重级别过滤；排查连接细节时改为 `info` 后重建主服务。

### 4. 更新已有独立 Docker 代理

主服务升级不会自动替换独立代理容器。对使用 Vision 的 Docker 目标或前置节点，在 Docker 代理页选择 **Xray** 后编辑保存，让应用重新生成配置并替换容器；随后运行该代理的 **HTTP** 测试。仅重启旧代理容器不会更新其核心或配置。Auto 在生成时跟随主服务核心，不会实时切换已有容器。

### 回滚

保留当前数据卷及数据库后端，把 Compose 的 `image` 改回已记录的旧版本或摘要，再执行 `docker compose up -d --no-deps --force-recreate v2raya-plus`。Vision 部署仍需选择 Xray；回到 v2.6.0 时手动开启 RouteOnly，避免重新采用原来有问题的组合。无需为本次补丁回滚数据库，也不要把旧归档覆盖到已经有新写入的数据卷。若降级到不支持当前数据库后端的更早程序，应先按 [SQLite 回滚文档](sqlite-runtime.md)处理。

---

# 测试与镜像发布

日常提交推送到 `main`、面向 `main` 的 Pull Request，以及手动运行工作流，都只执行测试，不向 GitHub Container Registry 发布镜像。手动运行时即使选择了已有 tag，也不会发布。这样可以持续验证代码，同时避免每次开发提交都增加 Packages 镜像版本。

镜像只在推送新的版本标签后发布，且必须先通过同一工作流的全部测试。支持 `vMAJOR.MINOR.PATCH`，以及可选的 SemVer 预发布后缀，例如 `v2.6.0-rc.1`；不接受前导零、缺失版本段或 `+build` 元数据。无效的 `v*` 标签会在登录镜像仓库前失败。

| 推送事件 | 测试 | 发布标签 |
| --- | --- | --- |
| `main` 分支 | 全部执行 | 不发布 |
| Pull Request → `main` | 全部执行 | 不发布 |
| 手动运行 | 全部执行 | 不发布 |
| `v2.6.0` | 全部执行 | `2.6.0`、`2.6`、提交号、`latest` |
| `v2.6.0-rc.1` | 全部执行 | `2.6.0-rc.1`、提交号；不更新 `2.6` 或 `latest` |

版本示例只说明标签格式，不代表已经创建或发布这些版本。每个发布镜像仍同时包含 Linux amd64 和 arm64 两种架构；Package 中可能同时显示镜像索引、架构镜像及构建证明记录，这些不是多次独立发布。

准备发布时，确认目标提交已审核、测试完成且工作区干净，再创建唯一的版本标签：

```bash
# 将示例版本替换成确定要发布的新版本。
git tag -a v2.6.0 -m "Release v2.6.0" "<verified-commit>"
git push origin v2.6.0
```

发布后检查该 tag 对应的工作流、镜像清单中的两个架构，以及 `org.opencontainers.image.revision` 是否指向目标提交。生产部署建议固定完整版本或镜像摘要；需要精确回滚时也可用该次发布的提交号标签。稳定版本的 `latest` 指向最近完成的稳定发布；不要重新推送或复用已经发布的版本标签。

旧的 Packages 版本和旧 `main` 镜像标签会保留，不会被此次规则变更删除；`main` 镜像标签以后也不再更新。已有部署若固定 `:main`，需要在实际升级时改为明确的正式版本或 `:latest`，不能把分支推送当作部署已更新。

规则位于[发布工作流](../.github/workflows/docker-publish.yml)。GitHub 的 job 条件控制是否运行发布任务，Docker metadata action 的 `latest=false` 禁用隐式 latest 标签，仅由稳定版本条件显式添加。[GitHub 工作流条件](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idif)、[Docker metadata action](https://github.com/docker/metadata-action)
