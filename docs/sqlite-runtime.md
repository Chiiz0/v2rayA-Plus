# SQLite 运行后端

主服务支持 Bolt 和 SQLite 两个运行后端，默认保留已有 Bolt 部署。SQLite 使用独立的可写 schema、WAL 和 `synchronous=FULL`，通过与 Bolt 相同的事务接口保存完整 KV 树。账号、节点、订阅、Docker 主节点和前置链、outbound、未知原始值和 uint64 sequence 均保留。没有双写，也没有改为上游的实体表或稳定节点 ID。

`v2raya-db` 随镜像提供；以下命令均要求停止该配置目录的主服务。工具持有目录实例锁及旧 Bolt 文件锁，不停止/启动服务，不修改 DNS，不操作代理容器。先核对实际 Compose、数据卷和钩子，并保存[完整部署备份](database-backup.md)。独立代理容器可以继续运行。

## 迁移已有部署

```bash
# 主服务停止后，从实际部署的 Compose 目录执行。
docker compose run --rm --no-deps --entrypoint v2raya-db v2raya-plus \
  runtime-migrate --config-dir /etc/v2raya-plus
docker compose run --rm --no-deps --entrypoint v2raya-db v2raya-plus \
  runtime-verify --config-dir /etc/v2raya-plus
docker compose start v2raya-plus
```

迁移完整读取当前 Bolt、审计引用、保存 `migration-<逻辑摘要>.json`，在新 SQLite 文件中提交并验证全部数据，再同步文件和目录，最后原子发布 `.v2raya-database.json`。该文件指定唯一运行后端及文件名。原 `bolt.db` 不修改；重复执行已完成的迁移会验证当前 SQLite，不会把新写入覆盖为原 Bolt 数据。

默认 `--database-backend auto`（环境变量 `V2RAYA_DATABASE_BACKEND`）遵循选择记录；没有记录的旧部署仍读取 `bolt.db`。显式 `bolt` 或 `sqlite` 是预期后端检查，必须与记录一致，不能绕过迁移。记录损坏、选中文件缺失、schema 不符或旧进程占用时，在 DNS 恢复和启动核心之前失败，不回退到旧数据。

首次使用 SQLite 的全新目录可执行 `runtime-init --config-dir /path/to/new-config`，再按正常参数启动主服务。目录必须已存在，建议权限 `0700`。首次服务启动用一个事务填入缺少的默认配置；错误会整体回滚。`runtime-init` 不接受已有应用数据，应改用迁移。

## 备份与恢复

```bash
# 主服务停止，输出必须是新路径。
v2raya-db runtime-export --config-dir /path/to/config --out /private/latest.json
v2raya-db verify --in /private/latest.json

mkdir -m 700 /path/to/restored-config
v2raya-db runtime-restore --config-dir /path/to/restored-config --in /private/latest.json
v2raya-db runtime-verify --config-dir /path/to/restored-config
```

`runtime-export` 读取选择记录，适用于两个后端。SQLite 源文件、WAL 和回滚日志在离线锁内复制到私有目录，复制前后检查文件身份、大小、时间和摘要；恢复及完整读取只发生在私有副本，源文件不交给 SQLite 写入。事务快照包含已提交的 WAL 写入，不能用单独复制 `.db` 替代。此 CLI 不提供在线备份。

SQLite 导出为 `v2raya-database-backup` 版本 `2`：`source.backend=sqlite`、`source.digestKind=logical-sha256`，`source.sha256` 是完整逻辑快照摘要，不冒充 SQLite 主文件的物理摘要。原 Bolt v1 归档仍受支持，旧 `export/verify/restore` 命令保持兼容。`runtime-restore` 接受 v1/v2，在新目录创建并选择 SQLite；`restore --in ... --db NEW_BOLT` 可从两种归档创建新 Bolt。

尚未首次启动的新安装会在 v2 归档中显式保存 `source.initializeDefaults`，恢复后仍会请求默认配置初始化。没有这个元数据时，同名未知桶或值均按原始数据保留，不会根据名字或内容推断初始化意图。归档中的密码哈希、节点凭据仍属私密数据；输出数据库、归档和选择记录权限为 `0600`。DNS 恢复记录、Docker 配置卷、Compose 和钩子继续单独备份，工具不会自动重建运行资源。

## 保留新写入的回滚

```bash
# 停止主服务，确认使用包含本功能的 v2raya-db。
v2raya-db runtime-rollback --config-dir /path/to/config
v2raya-db runtime-verify --config-dir /path/to/config
# 使用当前版本、相同配置目录重启主服务。
```

回滚从当前 SQLite（包括 WAL）生成最新完整归档，恢复到新的 `bolt-<摘要>[-<代号>].db` 并核验全部数据，然后选择这个新文件。它不会选回迁移前的 `bolt.db`。SQLite 和旧 Bolt 均保留；多次迁移/回滚遇到已被写入的历史文件时，发布新一代文件，不覆盖历史内容。工具不自动删除历史数据，需要归档清理时应先核对选择记录及完整备份。

如果还要降级到**不认识选择记录的旧程序**，必须先完成上述最新数据回滚，再把报告中 `runtime.database` 指向的最新 Bolt 恢复/安装为一个新配置目录的 `bolt.db`，同时按部署需要处理外部文件。直接让旧程序读取原目录中的旧 `bolt.db` 会读到过时数据。当前服务运行时持有该旧文件的共享锁来阻止旧程序并行写入；这个锁不替代停止后的正确降级步骤。全新 SQLite 安装也会保留一个空 Bolt 兼容锁文件，不应删除它。

## 失败恢复与边界

- 在新文件发布前失败：原运行库及选择记录保持不变。
- 新文件已发布、选择记录尚未切换：原后端仍有效；相同操作可验证并复用完整目标后重试。
- 选择记录已切换后中断：以记录指向的库为准，先运行 `runtime-verify`；不要手动删除记录或退回旧文件。
- 重试发现损坏文件、符号链接、冲突内容或应用引用错误：明确失败，不覆盖。文件同步失败可能发生在发布之后，应先核验报告和选择记录。

成功退出 `0`，文件/锁/schema/操作失败为 `1`，参数或应用数据审计失败为 `2`。导出可在应用审计失败时保留完整原始归档，并返回 `2`。完整性与逻辑摘要提供损坏检测，不认证数据来源。配置目录必须由可信本机用户控制；路径检查不能抵御拥有目录写权限的攻击者持续替换文件。

`sqlite-import/sqlite-verify/sqlite-export` 仍专用于[冻结候选](sqlite-migration.md)，只接受 v1 原归档；冻结候选和可写运行库的 application ID/schema 不同，不能相互冒用。数据库限制沿用归档的 128 MiB 原始数据、100,000 个桶和值、64 层嵌套；这次没有承诺性能提升。

## 验证

```bash
cd service
CGO_ENABLED=0 go test ./...
V2RAYA_TEST_DATABASE_BACKEND=sqlite go test ./server/service
go test -race ./common/instancelock ./db/... ./cmd/v2raya-db ./server/service
V2RAYA_TEST_DATABASE_BACKEND=sqlite go test -race ./server/service
CGO_ENABLED=0 go build -o /tmp/v2raya-runtime .
CGO_ENABLED=0 go build -o /tmp/v2raya-runtime-db ./cmd/v2raya-db
docker pull alpine:3.23
python3 ../scripts/test-sqlite-runtime.py --service /tmp/v2raya-runtime --db /tmp/v2raya-runtime-db
```

测试包括跨后端事务契约、并发快照、错误与 panic 回滚、完整未知树和无符号 sequence、Docker/前置链/outbound 引用更新、初始化原子性、选择记录与旧进程互斥、迁移发布阶段的进程中断、WAL 恢复和新写入后的反向转换。HTTP 脚本在无网络、无宿主端口和 Docker socket 的独立容器中运行真实主程序，覆盖注册登录、节点导入、Unicode 出站、重启持久性、回滚后登录及备份恢复；不启动代理核心。发布流水线在 Linux amd64/arm64 上实际运行静态 SQLite 及迁移测试。

已有真实部署备份的隔离副本也完成验收：原 6 桶、12 值逐值迁移后，通过主服务的 db/configure 路径读取；新写入和注入错误回滚后，完整归档、恢复出的最新 Bolt 和另一 SQLite 目录逻辑摘要一致。原备份 23 个文件及线上服务未被改动；真实数据不覆盖的订阅/前置链组合由测试夹具补充。线上切库仍需独立部署验收。
