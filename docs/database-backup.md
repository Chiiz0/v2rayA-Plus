# 离线数据库备份与恢复

`v2raya-db` 是独立的 Linux 命令，随 amd64/arm64 Docker 镜像提供。它不加载主服务配置，不启动核心、修改 DNS 或重建容器。运行后端仍为 BoltDB；完整归档也可用于[SQLite 离线迁移候选](sqlite-migration.md)的导入、验证和往返导出，不会切换后端。

## 备份范围

归档保存整个数据库的原始内容：`system`、节点与订阅、账号及原密码哈希、outbound 名称集合和设置、全部连接、Docker 主节点与前置节点引用，以及未知桶、未知键、嵌套桶、空值与桶 sequence。名字、键和值用 Base64 编码，避免非 UTF-8 字节或 JSON 大整数被转换。节点列表与连接顺序保留在原始值中。

格式标识为 `v2raya-bolt-backup`、版本为 `1`。清单包含源数据库文件 SHA-256、桶/值数量、值字节数、逐值 SHA-256 和覆盖完整结构的逻辑 SHA-256。逻辑摘要包含名字、层级、sequence、键和值，用于跨文件布局的完整比较；恢复后的 Bolt 文件不要求与源文件具有相同的物理文件摘要。摘要用于发现损坏和差异，不提供签名或来源认证。

归档包含节点凭据及账号哈希，输出权限为 `0600`，应保存在受控目录。输入必须是已有普通文件，文件本身不能是符号链接；所有输出都必须使用新路径，不提供覆盖选项。当前大小限制为 256 MiB 归档、128 MiB 原始名字/键/值合计、100,000 个桶和值、64 层嵌套，超限会明确失败。

DNS 恢复文件和 Docker 卷属于数据库之外的状态，必须分别处理，不能把数据库归档当作整个部署的备份。

## Docker Compose 操作

先更新到包含此工具的镜像。以下命令从项目的 Compose 目录执行；备份需要停止主服务，已有代理容器不会被此工具操作。只读导出也需要获取 Bolt 文件锁，默认等待 3 秒，仍被运行中的服务占用时会失败。

先核对实际部署的 Compose 文件和容器挂载。仓库默认配置使用端口映射；已有部署可能使用主机网络、自定义透明代理钩子、独立代理网络或不同的数据卷名。升级和恢复应保留这些部署约定，不能直接用仓库示例覆盖现有 Compose，也不能仅凭同名镜像标签判断运行内容相同。

```bash
mkdir -m 700 backups
docker compose stop v2raya-plus

docker compose run --rm --no-deps --entrypoint v2raya-db \
  -v "$PWD/backups:/backup" v2raya-plus \
  export --db /etc/v2raya-plus/bolt.db --out /backup/database-v1.json

docker compose run --rm --no-deps --entrypoint v2raya-db \
  -v "$PWD/backups:/backup" v2raya-plus \
  dns-backup --config-dir /etc/v2raya-plus --out /backup/host-dns-recovery.json

docker compose run --rm --no-deps --entrypoint v2raya-db \
  -v "$PWD/backups:/backup" v2raya-plus \
  verify --in /backup/database-v1.json --db /etc/v2raya-plus/bolt.db
```

每次备份使用新的文件名。`dns-backup` 仅复制固定文件 `resolv.conf.v2raya_backup` 的原始字节并返回摘要；源文件不存在时报告 `present: false`，不创建输出。它不会读取或写入主机 `/etc/resolv.conf`，也不会解析或执行恢复记录。DNS 副本独立于数据库快照，需在服务停止时分别保存；不要把原主机的恢复记录自动安装到另一台机器。

工具的结果为 JSON，退出码 `0` 表示成功，`1` 表示文件、锁、格式或其他操作失败，`2` 表示参数错误或数据审计未通过。`export` 在应用数据有问题时仍保存原始归档，再以 `2` 返回并列出问题，便于保留可用于排查的完整数据；不要只依据文件存在判断审计通过。

恢复演练写入备份目录中的新数据库，再逐值比对：

```bash
docker compose run --rm --no-deps --entrypoint v2raya-db \
  -v "$PWD/backups:/backup" v2raya-plus \
  restore --in /backup/database-v1.json --db /backup/restored.db

docker compose run --rm --no-deps --entrypoint v2raya-db \
  -v "$PWD/backups:/backup" v2raya-plus \
  verify --in /backup/database-v1.json --db /backup/restored.db

docker compose start v2raya-plus
```

这不会替换运行库。恢复会保留 `running` 等运行标志，因此将恢复文件安装到部署目录前仍需单独决定启用方式；本工具不自动启动部署，也不恢复容器卷。源文件和已有目标均不修改。

## 数据与外部依赖校验

`verify` 先校验归档格式、所有摘要和结构，再检查已知 JSON/gob 数据及位置引用。outbound 名称使用真实 gob 解码和集合键校验，支持 Unicode 与单字符名称。Docker 主节点、前置节点和每个 outbound 连接都检查原位置；坏 JSON/gob、越界引用和记录不一致会报告错误，`restore` 拒绝这些归档。未知桶和键保留并报告，值内未识别的字段也保持原始字节，不因无法解释而丢弃。

检查范围是数据结构和引用完整性，不代替内核协议合法性或网络连通性测试。历史记录若已错误指向另一个有效节点，无法仅凭位置判定原意。

显式添加 `--docker` 可只读核对当前 Docker 环境：

```bash
docker compose run --rm --no-deps --entrypoint v2raya-db \
  -v "$PWD/backups:/backup" v2raya-plus \
  verify --in /backup/database-v1.json --docker
```

该模式只调用带超时的容器和卷 `inspect`，区分存在、缺失与无法访问，并检查明确指定的内核镜像、配置卷挂载和 SOCKS 端口映射。它不会输出完整 inspect 数据，不执行容器内部命令、不创建辅助容器。检查发现缺失、无法核对或元数据差异时返回 `2`。

Docker 卷内的协议配置内容没有归档或比较，卷本身也不会恢复。节点、前置链的实际配置及网络可用性仍需在恢复演练中单独验证；`--docker` 通过仅代表上述元数据检查通过。

## 外部代理卷恢复演练

运行中的卷即使以只读方式挂载后执行 `tar`，也不是一致快照。主服务更新代理配置时会重写卷内文件，归档可能读到部分内容；数据库归档和卷归档也不自动属于同一时刻。真实备份应先协调停止配置写入，并与离线数据库备份配套保存。不要从运行中的 Bolt 文件复制一个“离线备份”。

先用合成数据和两个临时卷验证所选辅助镜像；之后才对已停止写入的真实源卷执行同样步骤。以下示例只导出源卷、恢复到全新的测试卷，不会接管或重建线上代理。将变量替换为已核实的源卷名、新归档名和固定版本的辅助镜像；归档必须来自可信来源。

```bash
SOURCE_VOLUME=v2raya-socks-12345
HELPER_IMAGE=alpine:3.23
ARCHIVE=backups/proxy-volume-unique.tar
docker volume inspect "$SOURCE_VOLUME" >/dev/null

# 新归档权限为 0600；已有文件会被 shell 的 noclobber 拒绝。
(umask 077; set -C
 docker run --rm --network none --read-only \
   --mount "type=volume,source=$SOURCE_VOLUME,target=/source,readonly" \
   "$HELPER_IMAGE" tar -C /source -cpf - . > "$ARCHIVE")

# 不传名称，让 Docker 创建一个全新随机命名的测试卷。
RESTORE_VOLUME=$(docker volume create --label v2raya.restore-test=true)
docker run --rm -i --network none --read-only \
  --mount "type=volume,source=$RESTORE_VOLUME,target=/restore" \
  "$HELPER_IMAGE" tar -C /restore -xpf - < "$ARCHIVE"
```

归档命令失败时应将该输出视为无效文件，不能继续恢复。辅助容器不挂载 Docker socket，也不开放网络端口。恢复后，在只读辅助容器中比较普通文件的 SHA-256、路径、权限和 UID/GID，确认空文件、目录结构与软链接目标；只输出摘要，避免输出节点凭据。再用独立测试容器验证协议配置，切勿自动接入线上网络或复用线上容器名。完成演练后只删除本次创建的测试卷。

隔离合成卷验收已覆盖上述内容及源卷保持不变。测试中的 BusyBox `tar` 保留普通文件时间戳，但不保留目录和软链接时间戳；ACL、扩展属性及其他卷驱动尚未验证，需要这些元数据时应另选支持它们的工具并补充比对。

另已在一次授权的真实部署短暂停机窗口完成数据库导出、源库比对及主数据卷和代理配置卷归档，随后立即重启原主服务；代理容器始终运行。新文件恢复的数据库逻辑摘要一致，两个全新测试卷中的文件摘要、权限和 UID/GID 与归档一致，原管理页面恢复 HTTP 200。测试卷已清理，未启动恢复出的核心；这证明该部署的数据恢复流程可行，不替代其他部署的协议连通性验收。实际 Compose、钩子及镜像和挂载元数据也应独立保存在私有备份目录中。

## 本地开发与验证

在 Linux 上，使用符合 `service/go.mod` 的 Go 工具链构建独立命令：

```bash
cd service
CGO_ENABLED=0 go build -o /tmp/v2raya-db ./cmd/v2raya-db
/tmp/v2raya-db export --db /path/to/offline/bolt.db --out /path/to/new-archive.json
/tmp/v2raya-db verify --in /path/to/new-archive.json
/tmp/v2raya-db restore --in /path/to/new-archive.json --db /path/to/new-restored.db
go test -race ./db/backup ./cmd/v2raya-db
```

导出只在持有只读 Bolt 锁期间读取和计算源摘要。恢复先验证归档，在目标同目录的临时 Bolt 中用单个事务写入，执行 Bolt 完整性检查及完整逻辑摘要比较，再同步文件并通过硬链接原子发布新目标、同步目录。目标已存在、校验失败、锁超时或写入失败均不会覆盖原文件；发布后若目录同步失败会明确报告目标已发布，保留完整目标供检查。文件系统不支持硬链接时会拒绝发布，不退化为可能覆盖目标的重命名。

这套工具使用临时数据和模拟 Docker 验收。切换 SQLite 之前，还需要在实际部署的离线副本上完成数据比对、容器卷恢复演练和后端迁移测试。
