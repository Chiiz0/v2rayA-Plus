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
git tag -a v2.6.0 -m "Release v2.6.0" <verified-commit>
git push origin v2.6.0
```

发布后检查该 tag 对应的工作流、镜像清单中的两个架构，以及 `org.opencontainers.image.revision` 是否指向目标提交。生产部署建议固定完整版本或镜像摘要；需要精确回滚时也可用该次发布的提交号标签。稳定版本的 `latest` 指向最近完成的稳定发布；不要重新推送或复用已经发布的版本标签。

旧的 Packages 版本和旧 `main` 镜像标签会保留，不会被此次规则变更删除；`main` 镜像标签以后也不再更新。已有部署若固定 `:main`，需要在实际升级时改为明确的正式版本或 `:latest`，不能把分支推送当作部署已更新。

规则位于[发布工作流](../.github/workflows/docker-publish.yml)。GitHub 的 job 条件控制是否运行发布任务，Docker metadata action 的 `latest=false` 禁用隐式 latest 标签，仅由稳定版本条件显式添加。[GitHub 工作流条件](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idif)、[Docker metadata action](https://github.com/docker/metadata-action)
