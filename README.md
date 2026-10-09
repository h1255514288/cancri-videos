# 视频分发管理系统

管理员上传视频、用户通过 API 领取、存储节点提供一次性下载的 Go 项目。

## 当前状态

✅ **Control 服务领取链路** - JWT 认证、幂等领取、限额控制、分类授权  
✅ **Node 服务下载链路** - JWT 授权、流式传输、状态机管理、重试保护  
✅ **测试通过** - 所有单元测试通过，覆盖率 81.6%（下载）、91.1%（API）、96.2%（认证）

管理员认证、用户/密钥/分类管理、共享存储文件上传与回收已实现。真实 PostgreSQL 集成测试、race 检查及 Docker/Compose 核心链路验收通过。当前面向单机共享存储部署，不是完整的多节点生产分发平台。

## 已实现功能

### Control 服务
- API Key 哈希认证（SHA256）、密钥有效期及用户状态检查
- 幂等领取（同用户同键同分类返回同一记录，跨分类冲突 409）
- Read Committed + 用户行锁 + 视频行锁 SKIP LOCKED
- 分类授权检查、每日额度控制（用户/用户组/系统默认继承）
- 待处理数量限制（reserved + streaming + retryable 未过期）
- JWT 下载凭证签发（链接 TTL、领取/重试窗口、密钥有效期取最小值）
- 查询按用户隔离，不暴露内部数据库标识

### Node 服务
- JWT 验证（HS256、过期时间、凭证版本）
- 领取状态检查（reserved/retryable 可下载，completed/expired/deleting 拒绝）
- 流式文件传输（Content-Type/Length/Disposition、X-Content-SHA256）
- 下载状态机（reserved/retryable → streaming → completed/retryable/deleting）；结束回写必须匹配启动时的 retry_count，拒绝旧尝试覆盖新尝试
- 重试保护（首次下载后 1 小时窗口，最多 6 次）
- 下载结束使用独立 10 秒超时同步持久化，下载中再次请求返回 409
- 节点启动要求 `VDS_NODE_ID`；凭证节点必须匹配
- 文件路径限制在存储目录内，校验视频 ID、文件类型与大小
- 不支持 Range 请求（Accept-Ranges: none）

### 通用基础设施
- PostgreSQL + sqlx 事务控制
- 健康检查（/health/live、/health/ready）
- 优雅关闭（SIGTERM 15s 超时）
- 环境变量配置（不读取 YAML 或 CONFIG_FILE）

## 快速开始

### 环境要求
- Go 1.21+（云沙箱已使用 Go 1.21.13 验证；Docker 构建使用 Go 1.26）
- PostgreSQL 15+
- Docker Compose（可选，用于本机开发数据库）

### 配置

现有配置加载器只读取环境变量。`configs/*.example.yaml` 是历史示例，不是已生效配置。

```bash
# 启动开发数据库（首次初始化时执行 scripts/init-db.sql）
# 必须先设置 VDS_DB_PASSWORD 和 VDS_JWT_SECRET
# Compose 核心链路已在 Linux 云沙箱验收
docker compose up -d --build

# Windows PowerShell 示例
$env:VDS_DB_HOST = "127.0.0.1"
$env:VDS_DB_PORT = "5432"
$env:VDS_DB_USER = "video_distribution"
$env:VDS_DB_PASSWORD = "dev_password"
$env:VDS_DB_NAME = "video_distribution"
$env:VDS_DB_SSLMODE = "disable"
$env:VDS_JWT_SECRET = "<至少32字符的随机密钥>"

# 启动服务
go run ./cmd/control   # 监听 127.0.0.1:8080
$env:VDS_NODE_ID = "1"
go run ./cmd/node      # 监听 127.0.0.1:8001
```

### 可用配置

| 环境变量 | 默认值 / 说明 |
|---------|--------------|
| `VDS_LISTEN_ADDR` | Control `127.0.0.1:8080`，Node `127.0.0.1:8001` |
| `VDS_SHUTDOWN_TIMEOUT` | `15s` |
| `VDS_DB_HOST` / `VDS_DB_PORT` | `localhost` / `5432` |
| `VDS_DB_USER` / `VDS_DB_PASSWORD` | `postgres` / 空 |
| `VDS_DB_NAME` | `video_distribution` |
| `VDS_DB_SSLMODE` | `disable` |
| `VDS_DB_MAX_CONNS` | `50` |
| `VDS_JWT_SECRET` | 必须自行设置 |

## API 使用

首次初始化数据库不再创建默认管理员。设置 `VDS_ADMIN_USERNAME`、`VDS_ADMIN_PASSWORD`（12–72 字节）及数据库/JWT 配置后执行 `go run ./cmd/bootstrap-admin`；已存在用户名不会被覆盖。已有数据库的管理员不会自动删除或改密。

管理端点使用 `/admin/login` 返回的 Bearer token，下载 token 不能用于管理接口。JSON 请求最多 4096 字节，仅接受一个对象，拒绝未知字段；无效参数返回 400、超大请求返回 413、重名分类返回 409。

管理端点：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/admin/login` | username/password 登录 |
| POST | `/admin/users` | 创建用户 |
| GET/PATCH | `/admin/users/<id>` | 查询/更新用户 |
| POST | `/admin/api_keys` | 为 user_id 创建密钥，原始密钥仅返回一次 |
| POST | `/admin/api_keys/<id>/revoke` | 撤销密钥 |
| POST/GET | `/admin/categories` | 创建/列出分类 |
| POST | `/admin/users/<id>/permissions/categories/<id>` | 分类授权 |
| POST | `/admin/videos` | multipart 上传：category_id、node_id、file |

上传上限 1 GiB；Control 必须能访问目标节点的存储目录，Compose 使用共享卷。上传完成后校验 SHA256 并创建 available 视频。当前不支持远程节点上传。

### 创建领取

```bash
curl -X POST http://127.0.0.1:8080/api/v1/claims \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Idempotency-Key: <UNIQUE_ID>" \
  -H "Content-Type: application/json" \
  -d '{"category_id": 3}'
```

**请求约束**：
- 标准请求头 `Idempotency-Key`，兼容 `X-Idempotency-Key`
- 幂等键去除两端空白后非空，最多 128 字节
- 分类 ID 必须为正整数
- 请求体最多 4096 字节，单个 JSON 对象

**响应结构**：
```json
{
  "claim_id": "clm_...",
  "status": "reserved",
  "video": {
    "id": "vid_...",
    "title": "示例视频",
    "filename": "example.mp4",
    "size_bytes": 104857600,
    "sha256": "...",
    "category_id": 3,
    "category_name": "示例分类"
  },
  "download": {
    "url": "http://localhost:8001/d/<JWT>",
    "expires_at": "2026-01-01T12:05:00Z",
    "supports_resume": false
  },
  "quota": { "daily_limit": 100, "used_today": 1, "remaining": 99 },
  "expires": {
    "claim_expires_at": "2026-01-01T12:10:00Z",
    "retry_deadline": null
  }
}
```

### 查询领取

```bash
curl http://127.0.0.1:8080/api/v1/claims/<CLAIM_ID> \
  -H "Authorization: Bearer <API_KEY>"
```

成功返回 200，包括 `claim_id`、`status`、`claimed_at`、`claim_expires_at`、`first_download_at`、`retry_deadline`、`completed_at`、`retry_count`。其他用户的领取与不存在记录均返回 404。

### 下载视频

```bash
curl -O http://127.0.0.1:8001/d/<JWT>
```

JWT 从领取响应的 `download.url` 获取。下载成功后领取状态自动更新为 `completed`，视频标记为 `consumed`。

## 规则与错误

### 限额规则
- 优先级：用户字段 > 用户组默认字段 > 系统默认（每日 100、待处理 3）
- 0 或负值禁止新增
- 每日额度按 UTC 日期计算
- 待处理统计：未过期 reserved + streaming + 窗口未过期且未耗尽次数的 retryable

### 幂等规则
- 同用户同键同分类返回同一领取，不重复扣额
- 跨分类冲突返回 409
- 重放仍需当前密钥、用户和分类权限有效，不受已耗尽限额阻止

### 下载规则
- reserved/retryable 状态可下载
- 首次下载后开启 1 小时重试窗口，最多 6 次
- 下载成功 → completed；失败且重试窗口有效、次数未耗尽 → retryable；否则 → deleting，不将失败误记为成功
- completed/deleting 状态的领取不可下载

### 已执行验证

云沙箱 PostgreSQL 15.19 / Go 1.21.13：领取并发/限额/回滚、Control HTTP、管理员登录与上传、真实文件下载及重复拒绝、物理删除、过期库存回收全部通过。`go test -race ./... -count=1` 通过。

### 状态码

| 状态码 | 场景 |
|-------|------|
| 200 | 成功 |
| 201 | 领取创建成功（包括幂等重放） |
| 400 | 输入、幂等键或领取 ID 不合法 |
| 401 | 缺少/无效/撤销/未生效/过期密钥，JWT 无效 |
| 403 | 用户禁用、分类未授权、每日或待处理上限 |
| 404 | 领取不存在/不属于用户、无可用视频 |
| 409 | 同键跨分类冲突、下载已进行中 |
| 410 | 领取已过期/已消费/重试耗尽 |
| 413 | 请求体过大 |
| 500 | 内部故障 |

## 测试

### 容器部署验收

有 Python 3、Docker daemon 和 Compose 插件的环境可执行：

```bash
python scripts/compose_smoke.py
```

脚本使用随机 Compose 项目名、临时随机密钥和独立主机端口，验证构建、启动、管理员初始化、上传、领取、下载内容、重复拒绝、重启持久化和实际文件回收。失败日志保存在 `smoke-reports/`；完成后停止并移除该项目容器，但保留卷，绝不删除已有项目数据。`--keep-running` 可保留容器用于排查。端口分配与实际绑定之间存在竞争，启动失败应查看日志。保留卷只含脚本生成的测试内容，清理须明确选择脚本打印的项目，勿执行全局 volume prune。

2026-10-09 已在 Linux 云沙箱使用 Docker 27.5.1、Compose 2.33.1 完整执行并得到 PASS，覆盖构建、启动、上传下载、重启持久化和物理回收。日志：`smoke-reports/compose-pass-20261009.log`。验收使用预拉取的镜像和 `VDS_BUILD_GOPROXY=https://goproxy.cn,direct`；Docker Hub 默认网络路径未验证可达。构建依赖源可通过 `VDS_BUILD_GOPROXY` 配置，默认保留官方源。运行镜像的 CA 证书从构建镜像复制，不再额外访问 Alpine 包仓库。

可选故障验收：`python scripts/compose_smoke.py --fault-tests`。额外上传 16 MiB 夹具，以慢客户端开始真实传输，验证活跃互斥，然后只强杀随机测试项目的 Node，验证连接截断、重启恢复、内容一致及尝试编号递增。当前故障模式已编写并通过辅助逻辑单元测试，但本轮镜像拉取与 daemon 查询超时，**尚未实际得到故障模式 PASS**；前述普通链路的历史 PASS 不替代新代码验收。

脚本预检与重启顺序单元测试：

```bash
python -m unittest discover -s scripts -p '*_test.py' -v
```

### 自动验证基线

`.github/workflows/ci.yml` 为将本目录作为 GitHub 仓库根目录时使用的配置：Ubuntu/Windows 检查、PostgreSQL 15 集成与 race、Compose 故障验收及报告 artifact。目前工作区不是 Git 仓库，未初始化或推送，远端 CI 尚未执行。workflow 不发布、不部署，仅授予 contents:read。

Makefile 提供 `test`、`vet`、`python-test`、`integration`、`compose-smoke` 入口；integration 仍必须显式设置专用数据库 URL。本轮删除任务预算回归已在真实数据库先失败后通过：耗尽的 failed 任务阻止自动创建新任务，不能通过每轮 cleanup 重置预算；没有自动重置历史失败任务。

### Go 测试

普通测试不连接数据库：

```bash
go build ./...
go vet ./...
go test ./... -count=1 -cover
```

真实数据库集成测试需显式开启：

```powershell
$env:VDS_TEST_DATABASE_URL = "postgres://<test-user>:<test-password>@127.0.0.1:5432/<test-database>?sslmode=disable"
go test -tags integration ./tests/integration -count=1 -v -timeout=2m
```

集成测试只接受 loopback 地址，创建随机 `vds_test_*` schema，结束时删除该 schema。进程被强制中止时可能残留。

Race 检查需要 CGO 和 C 编译器：

```bash
go test -race ./internal/control/... ./internal/node/... ./internal/shared/auth ./internal/shared/server
```

## 已知限制与待实现

1. Node 下载不支持 Range 请求（断点续传）
2. 未实现下载限速
3. 未实现并发下载数量限制
4. 清理任务每分钟执行；过期且未开始下载的预留在事务内回收库存，旧凭证版本失效。重试过期的记录转入删除状态，消费文件保留至少 1 小时后由所属 Node 删除。
5. 当前无管理前端，管理 API 尚未提供完整分页、删除及审计能力。
6. 单机共享存储恢复：Node 配置明确节点 ID 后，下载持有 `.download-locks` 中的操作系统文件锁直到完成回写；活跃持有者阻止重复下载，进程退出释放锁。取得锁的请求可在有效重试窗口内恢复遗留 streaming，并增加尝试编号。锁文件不能在服务运行时删除；所有 Node 实例必须共享同一实际目录和支持跨进程锁的文件系统。此机制未验收 NFS/SMB 或跨主机部署，不提供分布式锁保证。下载尝试审计和严格重试间隔仍未实现。
7. Docker/Compose 已完成 Linux 云沙箱核心流程验收；Windows Docker Desktop、跨主机和 NFS/SMB 未验收。TLS、监控、备份恢复和生产压力验收仍未完成。

## 项目结构

```
video-distribution-go/
├── cmd/
│   ├── control/          Control 服务入口
│   └── node/             Node 服务入口
├── internal/
│   ├── control/
│   │   ├── api/          HTTP API 处理器
│   │   └── claim/        领取服务逻辑
│   ├── node/
│   │   └── download/     下载处理器
│   └── shared/
│       ├── auth/         JWT、API Key 认证
│       ├── config/       配置加载
│       ├── db/           数据库连接
│       ├── model/        业务模型与规则
│       ├── server/       HTTP 服务器框架
│       └── testutil/     测试辅助
├── tests/integration/    集成测试
├── scripts/
│   └── init-db.sql       数据库初始化
├── configs/              配置示例（历史文件，不生效）
└── docs/go/              项目文档
```

## 许可证

待定。
