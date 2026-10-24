# 上游功能与存储迁移路线

评估基线：本地 `0c85b2f6`，上游 `1ee48148`。采用分阶段落地：先 XHTTP，再迁移数据库和界面。阶段 1（XHTTP）与阶段 2（Docker 引用一致性）已实现；阶段 3 已交付完整离线数据库工具，并完成一次真实部署的数据库与外部卷恢复验收；阶段 4 已实现独立实例锁、SQLite 冻结候选转换、运行时事务接口及 Bolt 适配器，并通过真实备份副本验收。当前运行库仍为 BoltDB，下一项是实现独立可写 SQLite schema 和运行适配器。

## 目标与收益

推进顺序为：**XHTTP → Docker 引用一致性 → 完整备份/迁移契约 → SQLite 离线迁移与可回滚运行 → 新界面渐进替换**。界面的 API 契约梳理可并行，界面升级不要求先切库。

SQLite 的价值是按节点读写、可查询的结构和跨对象事务，便于持续吸收上游存储改进；它不是现有一致性问题的自动修复，也没有本项目实测数据证明“换库即更快”。Bolt 已支持事务，本轮已将普通读取从 `Begin(true)` 改为只读事务，不再因查询创建空桶。列表解析与整份 JSON 重写仍是后续实体存储需要处理的开销。[运行时事务接口](../service/db/store/interface.go)、[列表操作](../service/db/listOp.go)、[节点定位](../service/db/configure/which.go)。

## 阶段 1：XHTTP

- 保留当前界面与存储形态，补齐 XHTTP 导入、编辑、分享、保存、生成配置及 Docker 代理路径。
- 验收节点参数往返，包括 VLESS encryption、传输参数与旧节点默认值；用实际目标内核验证生成配置，错误不得破坏已有容器。
- SQLite 与新界面按以下阶段推进，避免把协议支持绑定到一次全量架构升级。

实现采用原始 JSON 对象保存 `extra`，不直接复制上游固定字段投影，以保留未知字段、大整数及显式 `false`/`0`/`-1`。兼容标准 `mode`/`extra` 与旧 `xhttpMode`/`xhttpRawJson`/展开字段；显式空对象表示清除，避免旧 URI 把已清除的配置恢复回来。列表删除也保留未删除元素的原始 JSON。

下载分离连接同步透明代理绕行标记；Docker 前置代理使用传输层 `dialerProxy` 并覆盖下载连接。Docker 创建/替换前检查实际镜像版本及配置，失败或超时保留旧容器。验收包含真实 Xray 的普通高级参数、下载分离、前置代理加下载分离；最后一项使用相互隔离的网络，确认客户端不能绕过前置代理直达服务器。运行入口为 [`scripts/test-xhttp-docker.sh`](../scripts/test-xhttp-docker.sh)。

## 阶段 2：Docker 引用一致性（已实现）

`DockerProxy.Which/FrontWhich` 使用列表位置；此前删除与订阅更新没有同步调整 Docker 记录，删除前序节点或订阅重排后，容器重建可能选择另一个节点。[Docker 元数据](../service/db/configure/dockerProxy.go)、[删除路径](../service/server/service/touch.go)、[订阅更新](../service/server/service/subscription.go)。

本阶段保留磁盘 schema，完成以下修复：

1. 删除 Docker 正在引用的节点或其所属订阅时拒绝整次操作，错误列出相关端口，提示先改绑或删除代理；主节点、前置节点同等检查。
2. 删除其他节点/订阅时，按去重后的删除集合计算全部新索引；节点列表、主连接、`Which` 和 `FrontWhich` 在同一个 Bolt 写事务中提交。
3. 订阅刷新先在内存中计算旧节点到新节点的映射；被 Docker 引用的节点缺失或无法确定匹配时返回冲突，全部旧数据不变。匹配覆盖完整节点配置，不能只比较名称、地址或位置。
4. 坏的 Docker JSON 和无效引用报告错误，事务回滚；已经漂移的旧引用不能仅靠显示名自动猜回。
5. 订阅下载放在事务外；提交前验证旧订阅快照仍一致。Docker 创建、更新、恢复与节点修改使用同一服务层协调机制，避免配置生成后又保存过期索引；现有 controller 的 `updating` 标志未覆盖 Docker API 和后台刷新。

事务实现位于 [`db/configure/references.go`](../service/db/configure/references.go)，服务层使用统一互斥锁协调 Docker 生命周期、节点编辑、连接、出站与延迟回写。主内核从同一只读事务取得连接和对应节点，避免后台重载跨版本读取。订阅下载在锁外执行，后台排队任务也携带原始快照；提交冲突会提示重试，HTTP 错误响应不会被当成空订阅保存。

匹配使用规范化后的完整节点配置，包括凭据、协议参数、名称与未知扩展；排除延迟和 VMess/VLESS 的原始 URI 缓存。Docker 引用无法唯一匹配时拒绝整次刷新，先改绑或释放错误中列出的代理端口再重试。主连接沿用现有刷新选项：允许断开则移除缺失节点的连接，否则保留旧节点。

数据库事务内不执行 Docker 命令、下载或重启内核。仅重编号时保留容器和主内核配置；移除主连接后再重载，重载失败明确报告数据已提交，不把旧索引写回新列表。Docker 命令逐条设置 120 秒超时，写配置的临时容器使用唯一名称和独立超时清理，避免 Docker 无响应永久占住节点操作锁。Docker 命令和数据库不是跨系统事务，容器操作后的持久化错误也会向调用者返回。

回归使用临时 Bolt、假 Docker 和本地 HTTP，覆盖混合/重复删除、多个端口和出站（含 Unicode 与孤立 bucket）、双引用重排、缺失与重复节点、损坏 JSON/gob、快照冲突、暂停/恢复期间的并发删除。断言成功后引用仍解析到原节点，失败时所有持久化值不变，并检查未知字段、大整数与显式布尔值保留。

边界：本阶段协调服务端正在执行的操作，不为跨请求的旧界面索引提供身份校验。用户长期保留旧页面后提交的索引仍需后续 API 版本/预期身份或稳定 ID 契约解决；已经错误指向另一个有效节点的历史记录也无法自动辨认。

## 阶段 3：完整数据契约与备份

已提供独立 Linux 命令 [`v2raya-db`](../service/cmd/v2raya-db/main.go) 和版本化的完整字节契约，随 amd64/arm64 镜像构建；[使用方式与边界](database-backup.md)。工具不导入主服务运行时，不启动 DNS、核心或容器。现有 `Configure` 不是完整备份格式：不含 Docker 和完整 outbound 设置，`SetConfigure` 又分多次提交，不能直接用于迁库。[配置结构](../service/db/configure/configure.go)。

| 数据 | 必须保留的内容 |
| --- | --- |
| `system` | 所有键及原值，包括未知扩展、运行标志、DNS、路由、端口和设置 |
| `touch` | 独立节点与订阅顺序、协议完整 JSON、延迟、remarks、info、autoSelect |
| `accounts` | 原用户名和密码哈希；换库不强制重新注册，密码算法升级另行处理 |
| `outbounds` / `outbound.*` | 名称集合、设置、主连接列表及顺序；使用真正的 gob 解码器 |
| `dockerProxies` | 端口、双引用、core、名称、容器标识及状态；导入不自动重建容器 |
| 未知 bucket/key | 原始字节保留或明确拒绝迁移，禁止默默丢弃；嵌套结构也要检查 |

备份清单记录格式版本、源哈希、桶/键数量和逐值摘要；校验采用完整逻辑快照比较，而非只比较行数。独立保存 DNS 恢复文件；它属于原主机恢复状态，不随跨机器配置导入自动执行。容器配置卷不是数据库备份的一部分，恢复工具需报告外部容器/卷的存在性与配置差异。

当前实现逐字节保留以上数据和所有桶 sequence。导出使用有界只读文件锁；恢复在新临时文件中单事务写入，经 Bolt 完整性检查和逻辑摘要复核后，无覆盖地原子发布。`verify --docker` 可报告容器/卷存在性和内核镜像、挂载、端口映射差异；尚不归档或比较卷内的协议配置内容。`dns-backup` 独立复制恢复文件，不应用它。

测试夹具必须包含 Unicode/短 outbound 名称、空集合、旧格式节点、XHTTP/encryption、多个订阅与多端口 Docker、未知字段、损坏 JSON/gob、缺失引用；先在临时数据上证明导出→导入不丢信息，再使用用户数据库的只读副本验证。

真实部署验收已在授权的短暂停机窗口完成：导出与离线源比对通过；恢复到新文件后，全部桶、键值和 sequence 的逻辑摘要一致；主数据卷和代理配置卷恢复到全新测试卷后，文件摘要、权限及 UID/GID 一致。原主服务按原容器配置重启并恢复 HTTP 200，独立代理容器没有重启；测试资源已清理。Compose、钩子和部署元数据独立归档。验收范围和目录时间戳等限制见[备份文档](database-backup.md)，未启动恢复出的代理核心。

## 阶段 4：SQLite 运行基础与离线迁移

1. **独立实例锁（已实现）**：在迁移、DNS 恢复、数据库初始化和容器操作前持有配置目录锁，直到核心/DNS 清理和数据库关闭完成。SQLite WAL 允许多进程打开，不能替代目前 Bolt 的独占锁；启动仍以 2 秒超时取得已有源 Bolt 文件锁，识别不认识新锁文件的旧进程。旧进程仍运行时明确失败，不能恢复其 DNS。[启动入口](../service/startup.go)、[锁实现](../service/common/instancelock/lock.go)、[SQLite WAL](https://www.sqlite.org/wal.html)。
2. **驱动与发行矩阵（已实现）**：采用 `modernc.org/sqlite v1.61.0` / `modernc.org/libc v1.79.0`，实际引擎 `3.54.1`；模块最低 Go 1.26，工具链与 [Dockerfile](../Dockerfile) 固定 `1.26.10`，仍使用 `CGO_ENABLED=0`。升级范围为驱动所需依赖及旧格式化调用兼容修正，现有服务测试、真实 XHTTP 三场景、旧 QUIC TLS 1.3 和 gVisor 内存 TCP 回显已验证。[发行流水线](../.github/workflows/docker-publish.yml) 在发布前运行 Go/race 测试及 amd64/arm64 SQLite 实际运行测试。
3. 驱动只承诺维护最新版本；没有找到同时支持 Go 1.21 且覆盖所需修复的维护版本，因此升级工具链。所选版本除 SQLite WAL-reset 修复外，还包含近期共享内存映射和等待期间取消处理修复；`sqlite-info` 报告实际版本/source ID。当前冻结候选使用 `DELETE` 日志；未来运行时采用 WAL 时仍需单独测试恢复和备份。其他架构不在本轮发行矩阵内。[驱动变更](https://github.com/modernc-org/sqlite/blob/v1.61.0/CHANGELOG.md)、[维护策略](https://github.com/modernc-org/sqlite/blob/v1.61.0/SECURITY.md)、[SQLite WAL](https://www.sqlite.org/wal.html)。
4. **明确 schema 版本（候选 v1 已实现）**：SQLite 中保存完整 KV 树、原归档来源、清单和有序 schema 摘要；所有原始字节、未知结构和 uint64 sequence 保留。内部行号仅表达树关系，不是节点身份。运行时实体表、configure API 和 Docker 存储适配仍待下一阶段实现；冻结候选不能作为运行数据库安装。
5. **事务和连接池**：离线转换在单事务内创建 schema、写入数据和 ready 清单，读回全部内容才提交；每连接设置并核验 busy timeout、foreign keys、trusted schema 与 CHECK 约束。离线命令只需一条连接。运行时连接池须另行设计，不能直接把上游改为单连接：其订阅查询在外层 rows 未关闭时另查子节点，会阻塞；先合并查询或完成读取再查询。
6. **离线转换（已实现）**：导入完整归档，锁定候选所在目录，并以有界只读锁识别目录中仍被旧进程占用的 Bolt 文件。在同目录私有临时子目录中转换，验证完整快照、引用目标、`integrity_check`/`foreign_key_check`，以 `DELETE` / `FULL` 完成事务并关闭连接、同步文件后，无覆盖地原子发布。验证 SQLite 时使用安全打开的原文件生成私有只读副本，拒绝任何 WAL/SHM/journal 边文件。前置失败保留源和后端选择；[命令与限制](sqlite-migration.md)。
7. **崩溃与重试（候选已实现）**：准备数据只存在私有临时目录；全部内容与 ready 清单同事务提交并校验后，以硬链接发布单文件，并同步父目录。同归档重试完整验证数据与来源元数据、补齐目录同步后返回已存在；其他目标拒绝覆盖。已覆盖提交后、同步后、发布后的失败和进程退出，以及磁盘满事务回滚。崩溃遗留临时目录不会被当成已发布候选；没有自动清理未知遗留目录，也没有运行时后端选择文件。
8. **备份与回滚**：在线 SQLite 用 backup API 或 `VACUUM INTO`，不能只复制 `.db` 忽略 WAL。发布前保留完整 Bolt 快照；试运行未产生新写入时可切回该快照；产生新写入后须先导出当前逻辑数据并验证反向转换，不能拿旧快照冒充无损回滚。[官方备份方法](https://www.sqlite.org/backup.html)。

本阶段交付依次为：离线迁移命令和报告 → 测试目录/副本上的可选 SQLite 运行 → 备份恢复与回滚演练 → 默认切换。每次只保留一个权威写入后端，不双写。

**运行时事务适配已完成**：[`db/store`](../service/db/store/interface.go) 提供 `Store.View/Update` 及区分读写能力的事务接口，当前实现为启动流程持有的同一个 Bolt 句柄。应用层不再获取 `*bbolt.DB`；普通 JSON、列表、gob 集合、Docker 元数据、节点删除及订阅刷新均通过接口访问。多对象变更只开启一次 `Update`；回调返回错误或 panic 时全部回滚，不在事务内执行网络或容器操作。读取字节属于调用方，空值与不存在值保持区分，未知嵌套桶及 sequence 保留。损坏 gob 中的非字符串集合成员返回错误，不再 panic。

该接口目前只覆盖运行时实际使用的顶层桶操作，不代替完整归档接口。物理文件打开/关闭、实例互斥、报告副本和旧版导入仍由现有生命周期管理。已有故障注入证明 Docker 引用已更新后，后续出站/节点写入失败仍整体回滚；读取不修改事务版本、不创建缺失桶，并可在已有写事务期间读取一致快照。真实归档恢复副本上的应用读取与失败写入验收均保持完整文件字节和逻辑摘要不变，原备份及线上容器未改动。完整 Go、相关 race、XHTTP 实际网络回归通过，发行流水线增加双架构事务契约运行测试。

下一步才接入可写 SQLite 适配器和显式后端选择：冻结候选的原始 manifest/source 不能继续作为运行写入后的清单；须建立独立 runtime schema，并验证 WAL 一致备份、新写入后的反向转换以及现有事务契约。当前没有 SQLite 运行开关，不能把候选文件放到 `bolt.db` 路径启动。

已有真实备份完成了归档 → SQLite → 原归档 → 新 Bolt 验收：6 个桶、12 个值的完整逻辑摘要一致，返回归档与原归档逐字节相同；相同输入重试保持候选 inode/mtime。原备份 23 个文件的摘要和权限、所有者、修改时间不变；本次未停止或重启任何线上容器。真实数据未覆盖订阅与前置链的所有组合，相关结构由测试夹具补充。SQLite v1 尚无实体投影或运行时写入接口，这次验收不代表已完成生产切库或新写入后的回滚。

实例锁文件为 `.v2raya-instance.lock`，退出后保留，不能用删除文件的方式“解锁”；操作系统在关闭句柄或进程退出时释放锁。Linux/macOS/BSD 使用 `flock`，Windows 使用 `LockFileEx`；配置目录软链接别名共享锁，锁文件本身不允许软链接。已覆盖 Linux 跨进程竞争、异常退出、正常清理、旧 Bolt/v4 进程竞争、报告与重置密码入口；其他平台完成包级交叉编译，仍待实机运行验收。配置参数解析、目录创建和日志初始化可能发生在取得锁之前，但不会因此恢复 DNS 或启动内核。

`boltv4.db` 存在时持有其只读锁，迁移从该句柄读取并生成新文件快照，避免旧库单例无限等待或重复解析命令行。源文件不改写，新目标不覆盖；旧节点仍沿原来的分享链接/订阅重新导入流程转换，尚不等于 SQLite 阶段要求的全事务、可重试迁移。坏节点在发布前拒绝。`--report config`、`--report kernel` 与帮助报告保持可在服务运行期间使用；需要数据库的 `--report connection` 使用离线一致快照，库被占用时明确失败，不再直接复制运行中的数据库。报告临时文件退出时清理。

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
