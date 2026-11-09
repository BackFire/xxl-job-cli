# xxl-job-cli

面向自建 XXL-JOB 调度中心的 Go CLI。单个二进制、无第三方 Go 依赖，输出 JSON，适合人工和 AI Agent 调用。适配上游 **3.0–3.4** 的管理后台接口，另提供 **2.3.0** 接口适配。

它操作自建调度中心，不适用于阿里云托管 SchedulerX。上游当前的调度中心 OpenAPI 主要面向执行器，任务管理依赖管理后台 Web 接口；升级或定制部署可能改变接口。

## 安装

需要 Go 1.23 或更新版本：

```sh
go install github.com/BackFire/xxl-job-cli/cmd/xxl-job@latest
xxl-job --help
```

也可以在仓库中构建：`go build -o bin/xxl-job ./cmd/xxl-job`。二进制运行时不需要 Go。

## 配置

配置文件默认路径为 `$XDG_CONFIG_HOME/xxl-job/config.json`，未设置 `XDG_CONFIG_HOME` 时使用 `~/.config/xxl-job/config.json`。参考 [examples/config.json](examples/config.json)：每个 profile 指定管理端地址、用户名、密码所在环境变量名以及 API 家族。

```sh
mkdir -p ~/.config/xxl-job
cp examples/config.json ~/.config/xxl-job/config.json
chmod 600 ~/.config/xxl-job/config.json
# 将 endpoint 和 username 改为实际值，并通过密钥管理器注入密码变量。
xxl-job --profile dev config show
```

`config show` 只显示密码是否已配置，不输出密码。配置文件不接受密码字段；不要将真实密码存入仓库或命令行参数。profile 可以由 `--profile` 或 `XXL_JOB_PROFILE` 选择。

无需配置文件时，也可通过环境变量提供 `XXL_JOB_ENDPOINT`、`XXL_JOB_USERNAME` 和 `XXL_JOB_PASSWORD`。还支持 `XXL_JOB_API_VERSION`、`XXL_JOB_CONFIG` 和 `XXL_JOB_ALLOW_HTTP`（布尔值）。非空环境变量覆盖 profile；`--endpoint`、`--api-version` 和 `--allow-http` 覆盖相应配置。`XXL_JOB_PASSWORD` 优先于 profile 的 `passwordEnv` 指定的变量。

地址包含管理端的 context path，例如 `https://host/xxl-job-admin`。默认要求 HTTPS；本机 loopback 可使用 HTTP。对可信内网 HTTP，需要显式设置 profile 的 `"allowHttp": true`、`XXL_JOB_ALLOW_HTTP=true` 或 `--allow-http`。内网 HTTP 会明文传输登录信息。

`apiVersion` 根据实际版本选择：

| XXL-JOB | `apiVersion` | 登录 | 列表 / 写操作 |
|---|---|---|---|
| 2.3.0 | `2` | `/login` | `start/length`、`id` |
| 3.0.x | `3.0` | `/login` | `start/length`、`id` |
| 3.1.x | `3.1` | `/login` | `start/length`、`id` |
| 3.2.x | `3.2` | `/auth/doLogin` | `start/length`、`id` |
| 3.3.x、3.4.x | `3.3`、`3.4` 或默认 `3` | `/auth/doLogin` | `offset/pagesize`、`ids[]` |

不会自动探测或回退版本，避免将写请求发到错误接口。CLI 每次调用建立短期 Cookie 会话，不持久化会话，也不跟随 HTTP 重定向。

## 命令

全局选项放在资源名之前，命令选项放在资源名之后。`--help` 可查看命令参数。

```sh
xxl-job --profile dev group list --page 1 --page-size 20
xxl-job --profile dev job list --group 1 --status -1
xxl-job --profile dev job get --group 1 --id 42
xxl-job --profile dev job next --type CRON --schedule '0 0 2 * * ?'
xxl-job --profile dev log list --group 1 --job-id 42 --status 2
xxl-job --profile dev log get --id 123 --from-line 1
```

列表返回 `data`、`total`、`page`、`pageSize`；默认每页 20 条，不自动遍历。`job get` 会在指定组内逐页查找 ID。3.3.2 的日志列表要求指定 job ID，因此 `log list` 始终要求 `--group` 和 `--job-id`。API 2 的 `log get` 还需从日志列表提供 `--executor-address` 和 `--trigger-time`（Unix 毫秒）。

```sh
xxl-job --profile dev job create --file examples/job.json --dry-run
xxl-job --profile dev job create --file examples/job.json
xxl-job --profile dev job update --group 1 --id 42 --file examples/job-patch.json --dry-run
xxl-job --profile dev job update --group 1 --id 42 --file examples/job-patch.json
xxl-job --profile dev job start --id 42 --dry-run
xxl-job --profile dev job start --id 42
xxl-job --profile dev job stop --id 42
xxl-job --profile dev job trigger --id 42
xxl-job --profile dev job delete --id 42
```

`--file -` 从 stdin 读取 JSON。创建参数见 [examples/job.json](examples/job.json)，需要根据实际执行器、handler、负责人和调度表达式调整。创建后任务默认处于停止状态，是否启动由调用方决定。

更新接收顶层字段补丁：CLI 先查询完整任务，再合并并提交服务端的全部可更新字段；允许修改的字段由 `job update` 实现中的白名单限定。`jobGroup`、`id`、`glueType` 和 GLUE 源码不能通过 update 修改。可选字符串字段的 `null` 会作为缺失表单字段发送，空字符串则发送空值。读取和更新不是原子操作；并发修改可能互相覆盖。

`--dry-run` 输出请求形式，不提交写请求。`job update --dry-run` 仍需登录并读取当前任务，其他写命令的 dry-run 不连接服务。写请求不会自动重试；网络超时或连接断开时，服务端可能已经执行，应先检查状态或日志。`trigger` 成功表示调度请求被接受，不代表任务执行成功。

## 输出与权限

成功输出 JSON 到 stdout；失败退出码为 1，错误 JSON 写入 stderr。管理后台权限仍由服务端账户控制，`group list` 在上游 3.3/3.4 需要管理员角色。`job list` 和 `log list` 要求提供有权限的执行器组 ID。

任务参数、执行日志以及 dry-run 的表单可能包含业务敏感信息；CLI 不会对这些业务字段做脱敏。不要在公开场所粘贴完整输出。密码不出现在 `config show` 中，服务端错误消息中的密码及当前会话 Cookie 会被隐藏。

## Codex Skill

仓库附带可迁移的 [xxl-job Skill](skills/xxl-job/SKILL.md)。在仓库根目录执行以下命令可供新 Codex 会话自动发现：

```sh
mkdir -p ~/.codex/skills
ln -s "$PWD/skills/xxl-job" ~/.codex/skills/xxl-job
```

目标已存在时先检查，避免覆盖。Skill 使用已安装的 CLI 和外部配置，不包含真实账号、密码或实例地址。

## 验证范围

接口契约以官方 [3.0.0](https://github.com/xuxueli/xxl-job/tree/3.0.0)、[3.1.1](https://github.com/xuxueli/xxl-job/tree/3.1.1)、[3.2.0](https://github.com/xuxueli/xxl-job/tree/3.2.0)、[3.3.2](https://github.com/xuxueli/xxl-job/tree/3.3.2)、[3.4.0](https://github.com/xuxueli/xxl-job/tree/v3.4.0) 和 [2.3.0](https://github.com/xuxueli/xxl-job/tree/2.3.0) 源码为依据。自动化测试通过本地 HTTP 模拟服务验证认证、分页、创建、更新、启停、触发、日志和错误处理；CI 还针对上述版本的官方镜像运行端到端测试。其他补丁版本及厂商定制版本需要分别验证。

```sh
go test -race -cover ./...
go vet ./...
go build ./cmd/xxl-job
```
