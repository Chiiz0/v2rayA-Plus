# 上游功能与存储迁移路线

评估基线：本地 `d1b0081e`，上游 `1ee48148`。采用分阶段落地：先 XHTTP，再迁移数据库和界面。本文记录实施顺序与验收标准；当前运行库仍为 BoltDB，第一阶段交付 XHTTP 和迁移设计，不切换生产库。

## 目标与收益

推进顺序为：**XHTTP → Docker 引用一致性 → 完整备份/迁移契约 → SQLite 离线迁移与可回滚运行 → 新界面渐进替换**。界面的 API 契约梳理可并行，界面升级不要求先切库。

SQLite 的价值是按节点读写、可查询的结构和跨对象事务，便于持续吸收上游存储改进；它不是现有一致性问题的自动修复，也没有本项目实测数据证明“换库即更快”。当前 Bolt 已支持事务，主要额外开销来自读取也调用 `Begin(true)`、反复解析列表和整份 JSON 重写。可先独立改用只读事务、一次读取的节点定位器和批量写入。[本地读事务](../service/db/boltdb.go)、[列表操作](../service/db/listOp.go)、[节点定位](../service/db/configure/which.go)。

## 阶段 1：XHTTP

- 保留当前界面与存储形态，补齐 XHTTP 导入、编辑、分享、保存、生成配置及 Docker 代理路径。
- 验收节点参数往返，包括 VLESS encryption、传输参数与旧节点默认值；用实际目标内核验证生成配置，错误不得破坏已有容器。
- SQLite 与新界面按以下阶段推进，避免把协议支持绑定到一次全量架构升级。

实现采用原始 JSON 对象保存 `extra`，不直接复制上游固定字段投影，以保留未知字段、大整数及显式 `false`/`0`/`-1`。兼容标准 `mode`/`extra` 与旧 `xhttpMode`/`xhttpRawJson`/展开字段；显式空对象表示清除，避免旧 URI 把已清除的配置恢复回来。列表删除也保留未删除元素的原始 JSON。

下载分离连接同步透明代理绕行标记；Docker 前置代理使用传输层 `dialerProxy` 并覆盖下载连接。Docker 创建/替换前检查实际镜像版本及配置，失败或超时保留旧容器。验收包含真实 Xray 的普通高级参数、下载分离、前置代理加下载分离；最后一项使用相互隔离的网络，确认客户端不能绕过前置代理直达服务器。运行入口为 [`scripts/test-xhttp-docker.sh`](../scripts/test-xhttp-docker.sh)。

## 阶段 2：先修 Docker 引用一致性

`DockerProxy.Which/FrontWhich` 使用列表位置；现有删除与订阅更新只调整主进程连接，没有调整 Docker 记录。删除前序节点或订阅重排后，容器重建可能选择另一个节点。[Docker 元数据](../service/db/configure/dockerProxy.go)、[删除路径](../service/server/service/touch.go)、[订阅更新](../service/server/service/subscription.go)。

无需先增加稳定 ID 或改变磁盘 schema，即可交付以下最小修复：

1. 删除 Docker 正在引用的节点或其所属订阅时拒绝整次操作，错误列出相关端口，提示先改绑或删除代理；主节点、前置节点同等检查。
2. 删除其他节点/订阅时，按去重后的删除集合计算全部新索引；节点列表、主连接、`Which` 和 `FrontWhich` 在同一个 Bolt 写事务中提交。
3. 订阅刷新先在内存中计算旧节点到新节点的映射；被 Docker 引用的节点缺失或无法确定匹配时返回冲突，全部旧数据不变。匹配覆盖完整节点配置，不能只比较名称、地址或位置。
4. 坏的 Docker JSON 必须报告错误，不能像当前读取函数一样跳过后继续改动；已经漂移的旧引用不能仅靠显示名自动猜回。
5. 订阅下载放在事务外；提交前验证旧订阅快照仍一致。Docker 创建、更新、恢复与节点修改使用同一服务层协调机制，避免配置生成后又保存过期索引；现有 controller 的 `updating` 标志未覆盖 Docker API 和后台刷新。

改动边界：`db/listOp.go` 和 `db/configure/{configure,dockerProxy}.go` 提供事务内操作；`server/service/{touch,subscription,dockerProxy}.go` 调用纯映射函数与统一提交；不在数据库事务内执行 Docker 命令、下载或重启内核。保留现有容器配置，不因重编号重建容器。稳定 ID 留给后续独立设计，不作为本阶段前置。

契约用例：删除前序/后序/重复索引、同节点多端口、主与前置来自不同订阅、订阅乱序/重复节点/缺失节点、损坏记录、并发刷新与创建、提交失败。每项断言所有引用仍解析到原节点，或整次操作无写入；使用临时 Bolt 与假 Docker。

## 阶段 3：完整数据契约与备份

先交付可在副本上运行的导出、校验和导入工具，作为两种后端共享的验收标准。现有 `Configure` 不是完整备份格式：不含 Docker 和完整 outbound 设置，`SetConfigure` 又分多次提交，不能直接用于迁库。[配置结构](../service/db/configure/configure.go)。

| 数据 | 必须保留的内容 |
| --- | --- |
| `system` | 所有键及原值，包括未知扩展、运行标志、DNS、路由、端口和设置 |
| `touch` | 独立节点与订阅顺序、协议完整 JSON、延迟、remarks、info、autoSelect |
| `accounts` | 原用户名和密码哈希；换库不强制重新注册，密码算法升级另行处理 |
| `outbounds` / `outbound.*` | 名称集合、设置、主连接列表及顺序；使用真正的 gob 解码器 |
| `dockerProxies` | 端口、双引用、core、名称、容器标识及状态；导入不自动重建容器 |
| 未知 bucket/key | 原始字节保留或明确拒绝迁移，禁止默默丢弃；嵌套结构也要检查 |

备份清单记录格式版本、源哈希、桶/键数量和逐值摘要；校验采用完整逻辑快照比较，而非只比较行数。独立保存 DNS 恢复文件；它属于原主机恢复状态，不随跨机器配置导入自动执行。容器配置卷不是数据库备份的一部分，恢复工具需报告外部容器/卷的存在性与配置差异。

测试夹具必须包含 Unicode/短 outbound 名称、空集合、旧格式节点、XHTTP/encryption、多个订阅与多端口 Docker、未知字段、损坏 JSON/gob、缺失引用；先在临时数据上证明导出→导入不丢信息，再使用用户数据库的只读副本验证。

## 阶段 4：SQLite 运行基础与离线迁移

1. **独立实例锁**：在迁移、DNS 恢复、数据库初始化和容器操作前持有配置目录锁。SQLite WAL 允许多进程打开，不能替代目前 Bolt 的独占锁；迁移前还须以有限超时取得源 Bolt 文件锁，识别不认识新锁文件的旧进程。旧进程仍运行时明确失败，不能恢复其 DNS。[当前恢复入口](../service/pre.go)、[SQLite WAL](https://www.sqlite.org/wal.html)。
2. **驱动与发行矩阵**：本地模块声明 Go 1.21，[Dockerfile](../Dockerfile) 使用 `CGO_ENABLED=0`，[发行流水线](../.github/workflows/docker-publish.yml) 为 Linux amd64/arm64。选择驱动后分别验证编译、运行与 `SELECT sqlite_version()`；Go 工具链升级须单独验证现有网络依赖，不整体复制上游 Go 1.26 依赖集。
3. 上游锁定的 `modernc.org/sqlite v1.29.5` 内嵌 SQLite 3.45.1；迁移采用包含 WAL-reset 修复的引擎（3.51.3+，或已确认的补丁分支），并验证实际二进制版本。MIPS/Loong64 上游依赖 CGO 驱动，不应因此破坏现有无 CGO 的 amd64/arm64 发行；其他架构作为独立构建目标验证。[驱动版本](https://gitlab.com/cznic/sqlite/-/raw/v1.29.5/doc.go)、[官方修复说明](https://www.sqlite.org/wal.html#walreset)。
4. **明确 schema 版本**：建立有序 migration 记录；保留现有 configure API，单独适配 Docker 存储。第一版保留节点原始 JSON 与位置语义，避免同时引入 DNS、新 UI、账号算法和稳定 ID 的数据变更。添加实际查询需要的索引；不得把位置当 SQL 自增主键。
5. **事务和连接池**：每连接应用 busy timeout/foreign keys；多对象更新使用同一事务。WAL 仍只有一个 writer，写冲突须有明确策略。不能直接把上游连接池改为单连接：其订阅查询在外层 rows 未关闭时另查子节点，会阻塞；先合并查询或完成读取再查询。
6. **离线转换**：锁定配置目录，读取完整 Bolt 快照，在同目录临时 SQLite 文件中以单事务转换全部数据；验证完整快照、引用目标、`integrity_check`/`foreign_key_check`，关闭并完成 checkpoint、同步文件后再原子发布。任何前置失败都保留源文件及原后端选择。
7. **崩溃与重试**：使用含源摘要、目标摘要、schema 版本与迁移状态的清单，明确“准备/发布/完成”状态；文件及目录同步。两库并存时根据清单判定，不以“有一条 system 记录”猜测迁移完成；不覆盖已有备份，重复执行必须幂等。
8. **备份与回滚**：在线 SQLite 用 backup API 或 `VACUUM INTO`，不能只复制 `.db` 忽略 WAL。发布前保留完整 Bolt 快照；试运行未产生新写入时可切回该快照；产生新写入后须先导出当前逻辑数据并验证反向转换，不能拿旧快照冒充无损回滚。[官方备份方法](https://www.sqlite.org/backup.html)。

本阶段交付依次为：离线迁移命令和报告 → 测试目录/副本上的可选 SQLite 运行 → 备份恢复与回滚演练 → 默认切换。每次只保留一个权威写入后端，不双写。

## 阶段 5：新界面渐进替换

本地为 Vue 2/Buefy/Vuex/CLI，上游已改为 Vue 3/Vuetify/Pinia/TypeScript/Vite；新界面不依赖 SQLite，可以在数据库工作之后按独立阶段交付，也可并行梳理接口。上游的 Proxies 是出站组，未包含本项目 Docker 代理页面。

| 上游界面依赖 | 本地差异与接入要求 |
| --- | --- |
| `GET /account` | 本地缺少该会话检查；统一认证、过期登录和错误响应契约 |
| `PUT /outboundConnections`、`/outboundSelection` | 本地使用 `/connection` 与 Which；先做 API 适配，保留位置引用语义 |
| `/dnsRules`、`/customInbound`、`/params`、`/routingA/import` | 本地 DNS 为 `/dnsList` 的 internal/external；其余能力不能假定存在 |
| Docker、VLESS encryption、SSR | 上游界面未完整保留，须补齐本项目功能，不能用覆盖目录的方式迁移 |

首个 UI 交付是独立 `gui-next`：共享后端，先通过登录、节点与 Docker 端口 CRUD/前置链/内核选择/暂停恢复/真实容器 HTTP 测试。先抽取本地节点 codec 与 API 契约，再补设置、订阅、分享和协议能力矩阵；旧入口保持可用，通过后才替换默认入口。另测静态资源路径：本地 `/static` 路由剥前缀，上游构建使用 `assetsDir=static`，不能只以构建成功判断可加载。[本地界面](../gui/src/node.vue)、[上游 API](https://github.com/v2rayA/v2rayA/blob/1ee48148/gui/src/api/index.ts)。

分层参考 [89fb7d87](https://github.com/v2rayA/v2rayA/commit/89fb7d87) 的 API/session/store/codec、[0daa8263](https://github.com/v2rayA/v2rayA/commit/0daa8263) 的构建栈、[eb27dc89](https://github.com/v2rayA/v2rayA/commit/eb27dc89) 的页面骨架、[3841a1e7](https://github.com/v2rayA/v2rayA/commit/3841a1e7) 的弹窗与测试；删除旧界面是最后一步。

## SQLite 上游可复用部分与必须修正的差异

- 初版 [b013ec71](https://github.com/v2rayA/v2rayA/commit/b013ec71) 已涉及 14 个文件、约 1500 行新增；后续修复是迁移基线的一部分，不宜只摘初版。
- 必须吸收的经验：[e42174ca](https://github.com/v2rayA/v2rayA/commit/e42174ca) 订阅字段、[ba9d464c](https://github.com/v2rayA/v2rayA/commit/ba9d464c) 每连接 PRAGMA/LIKE 转义/并发 sort、[a042a45f](https://github.com/v2rayA/v2rayA/commit/a042a45f) 订阅单事务、[e24f102d](https://github.com/v2rayA/v2rayA/commit/e24f102d) outbound 与临时文件迁移、[48bcc093](https://github.com/v2rayA/v2rayA/commit/48bcc093) 清理遗留外键表。
- [当前上游 migrate.go](https://github.com/v2rayA/v2rayA/blob/1ee48148/service/db/migrate.go) 未迁移本项目 `dockerProxies`，明确跳过账号；outbound gob 用 ASCII 字符扫描，不能完整保留 Unicode/短名称；校验未覆盖全部订阅节点与全部值，必须替换为阶段 3 的契约。
- [当前上游 sqlite.go](https://github.com/v2rayA/v2rayA/blob/1ee48148/service/db/sqlite.go) 的只读模式直接拷贝主文件，不适合作在线备份；`synchronous=NORMAL` 的掉电持久性与 Bolt 默认同步不同，应明确选择并测试，而非当成无成本性能提升。[官方同步语义](https://www.sqlite.org/pragma.html#pragma_synchronous)。
- [ccfc5ca0](https://github.com/v2rayA/v2rayA/commit/ccfc5ca0) 的 typed store/NodeRef 可分离借鉴，但 NodeRef 仍为位置引用，不自动解决 Docker 稳定身份；无需连带迁入整个 kernel/DNS 架构。

## 发布门槛

- 两种后端同一契约：节点/订阅/账号/outbound/Docker/未知字段完整往返；旧 API 请求响应保持兼容。
- 删除、订阅刷新、并发延迟写入、Docker 创建/更新/恢复均不能产生串节点或半提交；旧数据缺陷要明确报告。
- 在转换中、提交后、发布前后模拟终止、磁盘满和权限错误；再次启动只能选中一个完整数据集，源备份不被覆盖。
- 第二实例、旧进程仍运行、配置目录锁失败不得触发 DNS 恢复；正常退出和失败退出释放锁。
- WAL 中仍有未 checkpoint 数据时备份并恢复，逐值对比；反向迁移保留新写入，无法转换的版本明确拒绝。
- amd64/arm64 无 CGO 镜像运行测试、内核配置验证、前端构建与 API 契约测试全部通过；新 UI 覆盖 Docker 前置代理、core 选择、暂停恢复、独立容器 HTTP 测试和协议参数后方可替换默认入口。
