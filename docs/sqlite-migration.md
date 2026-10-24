# SQLite 离线迁移候选

本阶段提供完整 KV 树的冻结 SQLite 副本，用于验证后续迁移的数据契约。主服务仍使用 BoltDB；这些命令不切换后端、不替换运行库，也不启动核心、应用 DNS 恢复记录或创建 Docker 容器。候选文件不是上游的运行时 SQLite schema，不能作为运行库安装。

先按[离线数据库备份与恢复](database-backup.md)取得并验证完整归档。已有一致归档可直接用于本阶段，无须再次停止线上服务。将候选放在单独的私有工作目录，保留原归档及其校验清单。

## 使用命令

```bash
mkdir -m 700 sqlite-candidate

# 报告当前工具实际使用的驱动、SQLite 引擎/source ID、Go 与 CGO 信息。
v2raya-db sqlite-info

v2raya-db sqlite-import \
  --in /path/to/database-v1.json --db sqlite-candidate/candidate.sqlite

v2raya-db sqlite-verify \
  --db sqlite-candidate/candidate.sqlite --in /path/to/database-v1.json

v2raya-db sqlite-export \
  --db sqlite-candidate/candidate.sqlite --out sqlite-candidate/returned.json

# 可选：回到一个新 Bolt 文件，再与原归档比较。
v2raya-db restore \
  --in sqlite-candidate/returned.json --db sqlite-candidate/returned-bolt.db
v2raya-db verify \
  --in /path/to/database-v1.json --db sqlite-candidate/returned-bolt.db
```

`sqlite-import` 先校验归档和应用数据引用。审计有错误时返回 `2`，不创建候选；未知数据的保留提示不阻止导入。新候选使用 `0600` 权限，完成事务、完整读取验证和同步后才无覆盖发布。

对相同路径重复导入同一归档时，工具先完整验证已有候选，再返回 `sqlite.alreadyPresent: true`。数据或来源元数据任一不同、文件损坏、目标类型不符都会拒绝，原文件保持不变。即使节点内容相同，重新导出的归档可能具有不同创建时间，因此不算同一输入。工具不会删除旧目标后重试。

如果进程在发布完成后退出，目标可能已经完整存在；保留它并执行 `sqlite-verify`，或使用同一归档重试导入。不要通过删除目标来处理不确定结果。

`sqlite-verify` 不带 `--in` 时检查候选内部 schema、数据库完整性、原始 KV 树、清单和应用引用。带 `--in` 时还比较原归档格式、版本、创建时间、Bolt 来源摘要和完整逻辑清单；`comparison.match` 必须为 `true`。只比较行数不能证明数据相同。

`sqlite-export` 写出新的完整归档，保留原始 Bolt 来源、创建时间、所有原始值及桶 sequence。归档中的 `source.sha256` 仍指向最初的 Bolt 文件；报告中的 `sqlite.sha256` 是 SQLite 候选文件的摘要，两者用途不同。它没有生成一个新的运行时快照。输出路径已存在时拒绝覆盖。

成功返回 `0`，文件、锁、格式和操作错误返回 `1`，参数、应用审计或与指定归档比较不通过返回 `2`。报告只含结构、数量、摘要和诊断，不输出节点凭据或账号哈希。归档和候选本身包含这些私密数据，工作目录应为 `0700`，文件应为 `0600`。

## 保存与验证的内容

SQLite v1 保留完整桶树，包括未知及非 UTF-8 名字、键和值、嵌套空桶、空值与 uint64 sequence。原始数据按字节保存，不将 JSON 数字转成浮点数，不重建订阅对象，也不改变账号密码哈希。节点与订阅顺序、outbound 名称集合及连接顺序、Docker 主节点和前置节点引用均留在原值中。

本阶段没有按节点、账号等实体拆表的投影，没有稳定节点 ID 的运行时写入接口，也不承诺运行性能提升。冻结副本完整往返通过后，才能继续设计运行时 schema 和切换策略。

候选使用单文件 `DELETE` 日志模式与完整同步，操作会取得所在目录的实例锁。验证先复制源文件到同目录的私有临时目录并核对源未变化，再由 SQLite 只读打开该副本；原文件不交给 SQLite 修改。目录仍需允许创建或打开稳定锁文件及临时副本，不要手动删除锁文件。发现 `-wal`、`-shm` 或 `-journal` 边文件时拒绝操作，不自动删除它们，也不尝试把运行库当作冻结候选读取。WAL 是数据库持久状态的一部分，单独复制主文件可能丢失已提交事务；未来在线备份应使用支持该状态的 SQLite 备份机制。[SQLite WAL](https://sqlite.org/wal.html)、[备份 API](https://sqlite.org/backup.html)

完整性校验和外键校验各有作用，`integrity_check` 不覆盖外键错误。清单比较仍用于证明 KV 字节、层级和 sequence 与原归档一致；摘要提供损坏检测，不认证归档来源。[SQLite PRAGMA](https://sqlite.org/pragma.html#pragma_integrity_check)

DNS 恢复记录、Docker 配置卷、部署 Compose 和钩子继续独立保存。本阶段不会因保存了 `running` 或 Docker 状态字段就恢复对应运行资源，协议连通性验证也不属于 SQLite 文件验证。

## 开发与验收

本阶段验收使用 Go 1.26.10、`modernc.org/sqlite v1.61.0` 和 SQLite 3.54.1；后续构建以 `service/go.mod` 及 `sqlite-info` 的实际输出为准。静态 Linux amd64/arm64 构建使用 `CGO_ENABLED=0`，主服务仍需完成升级工具链后的原有回归。

```bash
cd service
CGO_ENABLED=0 go build -o /tmp/v2raya-db ./cmd/v2raya-db
/tmp/v2raya-db sqlite-info
go test -race ./db/backup ./db/sqlitebackup ./cmd/v2raya-db
```

验收包括完整归档→SQLite→原归档→新 Bolt 的逻辑一致性，相同输入重试与冲突拒绝，未知字节、超出 int64 的 sequence、JSON 大整数及显式 `false/0/-1`，账号和 Docker 引用保留，损坏文件及边文件拒绝，以及源文件和已有目标不变。实际部署验收只使用已有一致备份的副本，在新的私有目录输出结果；不得覆盖原备份或自动切换线上后端。
