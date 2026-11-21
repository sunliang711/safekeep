# SafeKeep 服务架构与设计文档

> 本文档定义 **SafeKeep**（多云文件备份 HTTP 网关）的系统架构、数据库表结构、核心流程、API 规范与 Web 管理端规划。待确认后即可据此初始化代码骨架并分阶段开发。

---

## 1. 设计目标与技术选型

| 维度 | 选型 | 说明 |
| :--- | :--- | :--- |
| **后端语言** | Go 1.25+ | 高并发流式传输、单二进制部署、强类型 |
| **HTTP 路由** | `github.com/go-chi/chi/v5` | 轻量、原生 `net/http` 兼容、便于流式读写与中间件编排 |
| **数据库** | SQLite (WAL 模式) + `modernc.org/sqlite` | 纯 Go 实现免 CGO，单文件零依赖，支持并发读写与嵌入式队列 |
| **前端框架** | React + TypeScript + Vite + Tailwind CSS | 构建产物通过 `//go:embed` 打入 Go 二进制，所有运维操作均在 Web 端完成 |
| **定时调度** | `github.com/robfig/cron/v3` | 支持秒/分级别 Cron、运行时动态热更新与手动触发 |
| **上游协议** | `aws-sdk-go-v2` (S3 兼容) + Dropbox HTTP API v2 | 统一抽象为 `StorageProvider` 接口，便于后续扩展 R2 / OSS / Google Drive 等 |
| **代理支持** | `net/http.ProxyURL` + `golang.org/x/net/proxy` (SOCKS5) | 每个上游独立构建 `*http.Client`，支持 HTTP / HTTPS / SOCKS5 代理 |
| **凭证加密** | AES-256-GCM | 上游密钥、OAuth Token、代理账密加密落库 |

---

## 2. 系统总体架构与目录规划

### 2.1 架构简图

```text
                    +-------------------------------------------+
                    |             SafeKeep Single Binary        |
                    |                                           |
  Client App ------>|  Data API (/api/v1/*)                     |
  (API Key)         |   ├─ Auth + RateLimit + Idempotency       |
                    |   ├─ Stream Spooler (Disk + SHA-256)      |
                    |   └─ Sync Executor / Async Dispatcher     |
                    |            |                              |
  Admin Browser --->|  Web UI (go:embed) + Admin API            |
  (JWT Cookie)      |   ├─ Upstreams / Proxy / Dropbox OAuth2   |
                    |   ├─ Clients (API Keys & Policies)        |
                    |   ├─ Tasks (Monitor / Retry / Download)   |
                    |   ├─ Cron Scheduler (Retention & GC)      |
                    |   └─ Audit Logs                           |
                    |            |                              |
                    |  Worker Pool & Cron Engine                |
                    |   └─ Provider Registry (Per-Upstream      |
                    |       HTTP/SOCKS5 Transport)              |
                    +------------+---------------+--------------+
                                 |               |
                    [HTTP/SOCKS5 Proxy]     [Direct / Proxy]
                                 v               v
                           S3 / MinIO / R2    Dropbox
```

### 2.2 仓库目录结构

```text
safekeep/
├── cmd/safekeep/main.go             # 入口：初始化配置、主密钥、DB、队列、调度器、HTTP 服务
├── internal/
│   ├── config/                      # 启动参数（端口、数据目录）与动态系统设置读取
│   ├── crypto/                      # AES-256-GCM 加解密、API Key 生成与 SHA-256 哈希、密码哈希
│   ├── db/                          # SQLite 连接管理 (WAL)、Schema 迁移、Repository
│   ├── proxy/                       # HTTP / HTTPS / SOCKS5 代理拨号器与 *http.Client 工厂
│   ├── storage/                     # Provider 接口定义与注册表
│   │   ├── s3/                      # S3 及兼容存储适配器（支持分片上传与自定义代理 Client）
│   │   └── dropbox/                 # Dropbox 适配器（Upload Session、OAuth2 代理续期）
│   ├── backup/                      # 核心备份编排（落盘缓冲、同步/异步执行、恢复下载、幂等）
│   ├── queue/                       # 基于 SQLite 的持久化任务队列与 Worker Pool、重试退避
│   ├── scheduler/                   # Cron 调度器与内置定时任务（清理日志/临时盘/巡检）
│   ├── migration/                   # 环境迁移导出/预检/导入（口令加密打包与跨实例重加密）
│   ├── audit/                       # 异步/同步审计日志记录器
│   └── server/                      # HTTP 路由、中间件、Client API、Admin API、Embed 前端托管
├── web/                             # React + Vite + TS 管理后台源码
│   └── dist/                        # 构建输出目录，由 Go embed 嵌入
├── docs/DESIGN.md                   # 本文档
└── go.mod
```

---

## 3. 数据库表结构设计（SQLite WAL）

启动时自动启用 `PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;`。

### 3.1 `admins`（Web 管理员）
| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `id` | TEXT PK | ULID / UUID |
| `username` | TEXT UNIQUE | 登录用户名 |
| `password_hash` | TEXT | bcrypt/argon2id 哈希 |
| `is_active` | INTEGER | 1=启用，0=禁用 |
| `last_login_at` | DATETIME | 最近登录时间 |
| `created_at` / `updated_at` | DATETIME | 创建/更新时间 |

### 3.2 `system_settings`（Web 端全局系统配置）
| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `key` | TEXT PK | 如 `spool_dir`、`max_spool_bytes`、`worker_concurrency`、`sync_timeout_sec`、`public_base_url` |
| `value_json` | TEXT | 配置值（JSON） |
| `updated_at` | DATETIME | 更新时间 |
| `updated_by` | TEXT | 操作管理员 ID |

### 3.3 `upstreams`（上游存储与共享代理引用）
| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `id` | TEXT PK | 上游唯一 ID（如 `up_01...`） |
| `name` | TEXT UNIQUE | 展示名称（如 `prod-s3-us`、`team-dropbox`） |
| `type` | TEXT | `s3` \| `dropbox` |
| `enabled` | INTEGER | 是否启用 |
| `config_encrypted` | TEXT | AES-GCM 加密的 JSON（见下方说明） |
| `proxy_id` | TEXT NULL | FK -> `proxies.id`，NULL 表示直连；引用中的代理禁止删除 |
| `proxy_enabled` | INTEGER | 兼容旧版内嵌代理配置，读取时以 `proxy_id` 关联的代理为准 |
| `proxy_type` | TEXT | `http` \| `https` \| `socks5` |
| `proxy_host` | TEXT | 代理主机 |
| `proxy_port` | INTEGER | 代理端口 |
| `proxy_username_encrypted` | TEXT | 加密的代理用户名（可选） |
| `proxy_password_encrypted` | TEXT | 加密的代理密码（可选） |
| `last_test_status` | TEXT | `ok` \| `failed` \| `untested` |
| `last_test_error` | TEXT | 最近一次连通性测试错误信息 |
| `last_tested_at` | DATETIME | 最近测试时间 |
| `created_at` / `updated_at` | DATETIME | 时间戳 |

> **`config_encrypted` 明文 JSON 结构**：
> - **S3**：`endpoint`, `region`, `bucket`, `access_key_id`, `secret_access_key`, `base_prefix`, `force_path_style`, `storage_class`
> - **Dropbox**：`app_key`, `app_secret`, `refresh_token`, `access_token`, `token_expires_at`, `root_path`, `account_email`

### 3.4 `clients`（客户与 API Key）
| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `id` | TEXT PK | 客户 ID |
| `name` | TEXT UNIQUE | 客户名称 |
| `description` | TEXT | 备注 |
| `key_prefix` | TEXT | 明文前缀（如 `sk_a1b2c3d4`），用于列表识别 |
| `key_hash` | TEXT UNIQUE | API Key 的 SHA-256 哈希 |
| `enabled` | INTEGER | 是否启用 |
| `default_mode` | TEXT | `sync` \| `async`（未传 `mode` 参数时使用） |
| `path_prefix` | TEXT | 该客户强制隔离的存储前缀（如 `tenant-acme/`） |
| `max_file_size_bytes` | INTEGER | 单文件大小上限（0 表示跟随全局设置） |
| `allowed_mime_types_json` | TEXT | 允许的 MIME 白名单 JSON 数组（空表示不限） |
| `rate_limit_rpm` | INTEGER | 每分钟最大请求数（0 表示不限） |
| `webhook_url` | TEXT | 异步完成/失败回调地址（可选） |
| `webhook_secret_encrypted` | TEXT | 用于计算 `X-SafeKeep-Signature` 的加密密钥 |
| `expires_at` | DATETIME | API Key 过期时间（可选） |
| `last_used_at` | DATETIME | 最近调用时间 |
| `created_at` / `updated_at` | DATETIME | 时间戳 |

### 3.5 `client_upstreams`（客户与上游策略关联表）
> 支持“后台为客户配置默认上游策略组，同时允许请求参数指定其中的子集”。

| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `client_id` | TEXT | FK -> `clients.id` |
| `upstream_id` | TEXT | FK -> `upstreams.id` |
| `is_default` | INTEGER | 1=未传 `targets` 时默认扇出；0=仅在允许列表中，按需由请求指定 |
| `priority` | INTEGER | 恢复下载时的优先级（数字越小越优先） |
| `PRIMARY KEY` | | `(client_id, upstream_id)` |

### 3.6 `backup_tasks`（备份主任务）
| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `id` | TEXT PK | 任务 ID（ULID） |
| `client_id` | TEXT | FK -> `clients.id` |
| `idempotency_key` | TEXT | 幂等键（联合 `client_id` 唯一索引，可空） |
| `mode` | TEXT | `sync` \| `async` |
| `status` | TEXT | `pending` \| `running` \| `partial_success` \| `succeeded` \| `failed` |
| `original_filename` | TEXT | 原始文件名（清洗后） |
| `content_type` | TEXT | 文件 MIME 类型 |
| `file_size` | INTEGER | 文件字节数 |
| `sha256` | TEXT | 文件内容 SHA-256 |
| `spool_path` | TEXT | 本地临时缓冲文件路径 |
| `spool_cleaned` | INTEGER | 1=临时文件已清理 |
| `custom_subpath` | TEXT | 客户端请求的相对子目录 |
| `metadata_json` | TEXT | 客户端附带的业务自定义元数据 |
| `webhook_status` | TEXT | `none` \| `pending` \| `sent` \| `failed` |
| `error_summary` | TEXT | 汇总错误信息 |
| `created_at` / `started_at` / `finished_at` | DATETIME | 生命周期时间 |

### 3.7 `backup_task_items`（单上游执行子任务）
| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `id` | TEXT PK | 子任务 ID |
| `task_id` | TEXT | FK -> `backup_tasks.id` (ON DELETE CASCADE) |
| `upstream_id` | TEXT | FK -> `upstreams.id` |
| `upstream_name` | TEXT | 冗余快照名称，防止上游删改后丢失历史可读性 |
| `upstream_type` | TEXT | `s3` \| `dropbox` |
| `remote_path` | TEXT | 目标存储完整对象键/路径 |
| `remote_etag` | TEXT | 上游返回 ETag / content_hash |
| `remote_version_id` | TEXT | 上游版本 ID / rev（如有） |
| `status` | TEXT | `pending` \| `running` \| `succeeded` \| `failed` |
| `attempt_count` | INTEGER | 已尝试次数 |
| `max_attempts` | INTEGER | 最大重试次数 |
| `next_retry_at` | DATETIME | 下次指数退避重试时间 |
| `duration_ms` | INTEGER | 本次耗时 |
| `error_message` | TEXT | 失败原因 |
| `started_at` / `finished_at` | DATETIME | 执行时间 |

### 3.8 `audit_logs`（审计日志）
| 字段 | 类型 | 说明 |
| :--- | :--- | :--- |
| `id` | INTEGER PK | 自增 ID |
| `actor_type` | TEXT | `admin` \| `client` \| `system` |
| `actor_id` | TEXT | 管理员 ID / 客户 ID / `scheduler` |
| `actor_name` | TEXT | 操作者名称快照 |
| `action` | TEXT | 动作标识（如 `backup.upload`、`upstream.create`、`client.rotate_key`、`cron.run`） |
| `resource_type` | TEXT | 资源类型（`backup_task`、`upstream`、`client`、`cron_job`、`setting`） |
| `resource_id` | TEXT | 目标资源 ID |
| `status` | TEXT | `success` \| `failure` |
| `ip_address` | TEXT | 客户端 IP |
| `user_agent` | TEXT | UA |
| `request_id` | TEXT | 请求追踪 ID |
| `detail_json` | TEXT | 脱敏后的上下文（文件名、大小、耗时、配置变更字段等） |
| `created_at` | DATETIME | 发生时间（建索引用于定时清理与筛选） |

### 3.9 `cron_jobs` 与 `cron_job_runs`（定时任务与执行记录）
- **`cron_jobs`**：`id`, `job_key` (UNIQUE), `name`, `description`, `cron_expr`, `enabled`, `config_json`, `last_run_at`, `last_status`, `last_duration_ms`, `last_summary`, `next_run_at`, `updated_at`
- **`cron_job_runs`**：`id`, `job_id`, `trigger_type` (`schedule` | `manual`), `triggered_by`, `status`, `affected_rows`, `output_log`, `started_at`, `finished_at`

---

## 4. 核心机制与关键细节

### 4.1 首次启动与零手工配置原则
1. 启动时若未设置环境变量 `SAFEKEEP_MASTER_KEY`，程序自动在数据目录生成 32 字节随机主密钥文件 `master.key`（权限 `0600`），用于加密所有上游凭证与代理密码。
2. 若 `admins` 表为空，Web 端自动进入**初始化向导（Setup Wizard）**，引导创建第一个管理员账号，无需修改配置文件或执行 CLI 命令。

### 4.2 每上游选择网络代理（HTTP / HTTPS / SOCKS5）
代理配置独立存入 `proxies` 表：`id`、唯一 `name`、`type`、`host`、`port`、加密的 `username_encrypted` / `password_encrypted`、`created_at` / `updated_at`。上游通过可空的 `proxy_id` 引用代理，多个上游可以共用；空引用表示直连。更新代理时在同一事务中更新引用上游的版本并重置测试状态，使 Provider 缓存于下次请求重建。外键与删除接口共同保护被引用的代理。

启动升级时，旧内嵌代理在事务中转换为独立记录并绑定，保留原认证密文，重复启动不重复转换。迁移包增加可选代理列表和上游引用，包含未使用的代理；仍支持旧包导入，并保留内嵌快照以兼容旧版读取。合并时按 ID / 名称匹配代理，并映射上游引用。

为每个 `Upstream` 实例构建独立 `http.Client`：
- **HTTP / HTTPS 代理**：构造 `url.URL`（含可选账密），设置 `Transport.Proxy = http.ProxyURL(u)`。
- **SOCKS5 代理**：使用 `golang.org/x/net/proxy.SOCKS5("tcp", hostPort, auth, proxy.Direct)`，将 `DialContext` 注入 `http.Transport`。
- **关键点**：
  - S3 SDK (`aws-sdk-go-v2`) 通过 `config.WithHTTPClient(proxyClient)` 注入。
  - Dropbox 的文件请求**以及 OAuth2 授权换取 Token / Refresh Token 请求**均通过 `context.WithValue(ctx, oauth2.HTTPClient, proxyClient)` 注入同一个代理 Client，确保代理环境下授权与续期稳定可用。
  - 若上游修改了代理或凭证，自动失效并重建内存中的 Provider 客户端缓存。

### 4.3 Dropbox Web 端 OAuth2 授权流程
1. 管理员在 Web 端填写 Dropbox 的 `App Key`、`App Secret`、根目录及代理配置并保存上游。
2. 管理员点击「授权连接 Dropbox」，前端请求 `/admin/api/v1/upstreams/{id}/oauth/dropbox/start`。
3. 后端生成防 CSRF 的 `state`（关联 `upstream_id`），返回 Dropbox 授权跳转地址（`token_access_type=offline` 以获取长期 `refresh_token`）。
4. Dropbox 回调 `/admin/api/v1/oauth/dropbox/callback?code=...&state=...`，后端**使用该上游配置的代理**向 Dropbox 换取 `refresh_token` 与账号邮箱并加密入库，跳转回 Web 管理页显示已授权状态。

### 4.4 同步与异步双模式执行流
```text
Client POST /api/v1/backups?mode=sync|async&targets=up_1,up_2
  │
  ├─ 1. 鉴权 (X-API-Key / Bearer) + 限流检查 + Idempotency-Key 查重
  ├─ 2. 校验 targets：未传取 is_default=1 上游；传入则校验均在 client_upstreams 白名单内
  ├─ 3. 安全清洗文件名 & sub_path（禁止 ".." 与绝对路径），拼接客户 path_prefix
  ├─ 4. 流式写入临时缓冲盘 spool_path，同时计算 SHA-256 与校验 max_file_size_bytes
  ├─ 5. 写入 backup_tasks + backup_task_items
  │
  ├─ 若 mode == "async"：
  │     └─ 立即返回 202 Accepted { task_id, status: "pending", items: [...] }
  │        后台 Worker Pool 异步并发上传各 item，支持失败退避重试与 Webhook 回调
  │
  └─ 若 mode == "sync"：
        └─ 同步并发上传所有 item（受 sync_timeout_sec 保护）
           ├─ 全部成功：清理 spool，返回 200 OK + 各上游路径/ETag
           ├─ 部分/全部失败：返回 207 Multi-Status / 502（含各上游明细）；
           │  未成功的 item 保留 spool 并允许后续自动重试或 Web 端手动重试
           └─ 同步超时：转入后台继续完成（返回 202 或 504 附带 task_id 供查询）
```

> **崩溃恢复**：服务启动时自动扫描 `status = 'running'` 的遗留任务与子任务，重置为 `pending` 重新入队执行。

### 4.5 内置定时任务清单（可在 Web 端修改 Cron 与参数）
| `job_key` | 默认周期 | 用途说明 |
| :--- | :--- | :--- |
| `cleanup_audit_logs` | `0 2 * * *` (每日 02:00) | 删除超过保留天数（默认 90 天）的审计日志 |
| `cleanup_task_records` | `0 3 * * *` (每日 03:00) | 归档/清理超过保留天数（默认 180 天）的已完结任务与 Cron 执行记录 |
| `cleanup_orphan_spool` | `0 * * * *` (每小时) | 清理临时缓冲目录中超时残留或已完成但未删除的孤儿文件 |
| `upstream_health_check` | `*/30 * * * *` (每 30 分钟) | 巡检已启用上游及代理连通性，异常时更新状态供仪表盘告警 |
| `dropbox_token_refresh` | `0 */4 * * *` (每 4 小时) | 提前刷新即将过期的 Dropbox Access Token，验证 Refresh Token 有效性 |

### 4.6 迁移导出与导入（快速恢复环境）

为了在服务器迁移、灾备重建或环境复制时实现“一键恢复”，系统提供基于**口令加密的可移植迁移包（`.skbundle`）**，避免直接拷贝 SQLite 与 `master.key` 带来的密钥耦合和跨平台风险：

1. **导出范围（Web 端按需勾选）**：
   - **核心配置（必选）**：`system_settings`、`upstreams`（含代理与凭证）、`clients` + `client_upstreams`（保留 `key_hash` 与 `key_prefix`，**现有客户端 API Key 迁移后无需更换即可继续使用**）、`cron_jobs`。
   - **可选数据**：`backup_tasks` + `backup_task_items`（历史备份文件索引与远程路径——导入后新环境可直接对旧文件执行检索与恢复下载）、`admins`、`audit_logs`、`cron_job_runs`。
2. **跨实例安全重加密**：
   - **导出时**：服务端使用当前环境的 `master.key` 解密上游密钥、Dropbox Refresh Token、代理账密与 Webhook Secret，连同元数据与版本校验头打包为 JSON，再使用管理员输入的**临时迁移口令**（经 Argon2id/PBKDF2 派生密钥 + AES-256-GCM）整包加密后下载。
   - **导入时**：目标环境使用迁移口令解密并校验包完整性，再用目标环境自身的 `master.key` **重新加密所有敏感字段**后写入数据库。
3. **预检（Dry-Run）与恢复模式**：
   - 上传迁移包并校验口令后，先展示**预检摘要**（包版本、导出时间、各表条目数、与当前环境存在 ID/名称冲突的项）。
   - 支持两种导入策略（全过程在单一 SQLite 事务内执行，失败自动回滚）：
     - **`replace`（灾难恢复/全新覆盖）**：清空目标表后按导出快照完整重建。
     - **`merge`（增量合并）**：保留现有配置，对冲突项可选择「跳过」或「以导入包覆盖」。
4. **热重载与审计**：
   - 导入提交成功后自动失效内存中的上游 Provider 缓存、重新加载 `cron_jobs` 调度器配置，并在 `audit_logs` 记录导出/导入操作审计。

---

## 5. HTTP API 规范设计

### 5.1 客户端数据面 API（使用 `X-API-Key` 或 `Authorization: Bearer <api_key>`）

| 方法与路径 | 说明 | 核心参数 / 响应 |
| :--- | :--- | :--- |
| `POST /api/v1/backups` | 上传并备份文件 | **Query/Form**：`mode` (`sync` \| `async`)、`targets` (逗号分隔上游 ID 或名称)、`path` (相对子路径)、`metadata` (JSON)<br>**Header**：`Idempotency-Key`（可选）、`X-Content-SHA256`（可选校验）<br>**Body**：`multipart/form-data` (`file` 字段) 或 raw stream + `X-File-Name`<br>**响应**：`200` (sync 全成功) / `207` (sync 部分成功) / `202` (async 已接收) |
| `GET /api/v1/backups/{task_id}` | 查询备份任务状态 | 返回任务总状态、文件 SHA-256、各上游子任务状态与远程路径 |
| `GET /api/v1/backups` | 查询当前客户的备份列表 | 支持分页、`status`、`filename`、`sha256` 过滤 |
| `GET /api/v1/backups/{task_id}/download` | 从上游恢复下载文件 | 可选 `?upstream_id=...`，默认按客户优先级选择首个成功上游流式回源（走对应上游代理） |
| `GET /api/v1/upstreams` | 查看当前 API Key 可用的上游列表 | 仅返回脱敏后的 ID、名称、类型、是否默认 |

### 5.2 Web 管理端 API（使用登录态 Cookie / JWT，统一前缀 `/admin/api/v1`）

- **初始化与认证**：
  - `GET /setup/status`、`POST /setup/init`（仅首次无管理员时可用）
  - `POST /auth/login`、`POST /auth/logout`、`GET /auth/me`、`PUT /auth/password`
- **仪表盘**：
  - `GET /dashboard/summary`（今日/近 7 天备份量与成功率、队列深度、缓冲盘占用、上游健康状态）
- **共享代理管理**：
  - `GET /proxies`、`POST /proxies`、`PUT /proxies/{id}`、`DELETE /proxies/{id}`
  - `GET /proxies/{id}/secrets`（管理员查看密码，记录审计）
  - 上游写入时传 `proxy_id`；空字符串表示直连，省略时保留既有选择。
  - `POST /upstreams/test-proxy` 支持 `proxy_id` 测试已保存代理；编辑代理时仍可提交待保存字段。
- **上游存储管理**：
  - `GET /upstreams`、`POST /upstreams`、`PUT /upstreams/{id}`、`DELETE /upstreams/{id}`
  - `POST /upstreams/{id}/test`（测试上游存储与 HTTP/SOCKS5 代理连通性）
  - `GET /upstreams/{id}/oauth/dropbox/start`、`GET /oauth/dropbox/callback`
- **客户与 API Key 管理**：
  - `GET /clients`、`POST /clients`（创建时返回一次完整明文 API Key）、`PUT /clients/{id}`、`DELETE /clients/{id}`
  - `POST /clients/{id}/rotate-key`（轮换并吊销旧 Key，返回一次新明文 Key）
  - `PUT /clients/{id}/upstreams`（配置该客户的默认上游与允许子集）
- **备份任务与文件恢复**：
  - `GET /tasks`、`GET /tasks/{id}`
  - `POST /tasks/{id}/retry`（重试失败或部分失败的子任务）
  - `GET /tasks/{id}/download`（管理员代理下载恢复文件）
- **定时任务管理**：
  - `GET /cron-jobs`、`PUT /cron-jobs/{id}`（更新 Cron 表达式、启停状态、保留天数等配置）
  - `POST /cron-jobs/{id}/run`（立即手动执行一次）
  - `GET /cron-jobs/{id}/runs`（查看执行历史与日志）
- **审计日志**：
  - `GET /audit-logs`（按时间、actor、action、status、关键词筛选）、`GET /audit-logs/export`（导出 CSV/JSON）
- **系统设置与管理员**：
  - `GET /settings`、`PUT /settings`
  - `GET /admins`、`POST /admins`、`PUT /admins/{id}`
- **迁移导出与导入**：
  - `POST /migration/export`（传入导出范围与加密口令，流式下载 `.skbundle` 加密包）
  - `POST /migration/inspect`（上传迁移包与口令，执行解密校验并返回资源统计与冲突预览）
  - `POST /migration/import`（上传迁移包、口令与策略 `replace|merge`，事务导入并热重载调度器/缓存）

### 5.3 运维探针
- `GET /healthz`（进程存活）、`GET /readyz`（SQLite 读写与缓冲目录可写检查）

---

## 6. Web 管理端页面规划

所有操作均可在 Web UI 完成，拟规划以下 7 个主菜单页面：
1. **概览仪表盘（Dashboard）**：任务成功率趋势、排队数、各上游健康状态灯、本地缓冲盘水位、最近失败告警。
2. **备份任务与文件（Backups & Tasks）**：查看文件列表与各上游同步进度、查看错误堆栈、单任务/批量重试、直接下载恢复文件。
3. **上游存储与代理（Upstreams）**：管理 S3 / Dropbox 实例；配置 HTTP/SOCKS5 代理（含账密）；一键连通性测试；Dropbox Web OAuth2 一键授权绑定。
4. **客户与 API Key（Clients）**：签发/轮换/禁用 API Key；绑定默认上游组与可选上游白名单；设置路径前缀、文件大小/MIME 限制、RPM 限流与 Webhook。
5. **定时任务（Scheduler）**：启停任务、编辑 Cron 表达式与清理保留天数（如审计日志保留天数）、一键“立即运行”、查看历史输出。
6. **审计日志（Audit Logs）**：区分管理端操作与 API 上传记录，多维筛选、查看详情 JSON、导出报表。
7. **系统设置与迁移（Settings & Migration）**：临时缓冲目录、同步超时时间、并发 Worker 数、公网回调 Base URL（用于 Dropbox OAuth）、管理员账号管理，以及**环境迁移导出（口令加密下载）与导入预检/一键恢复**。

---

## 7. 已确认的实现决策

1. **文件名冲突策略**：默认在远程文件名自动追加时间戳与 Task ID 前缀（如 `20261119_01Jxxx_report.pdf`），避免覆盖同名历史文件。
2. **同步超时兜底**：`mode=sync` 超时后自动转为后台异步任务继续上传，并返回 `202 Accepted` 及 `task_id` 供查询。
3. **SHA-256 内容去重（秒传）**：同一客户再次上传相同 SHA-256 文件时，若目标上游已有成功备份记录，直接清理临时文件并返回成功（`deduplicated: true`），避免重复上传；若仅部分目标已备份，则仅补传缺失的目标上游（可通过 `?dedup=false` 显式跳过）。
4. **前端技术栈**：React + TypeScript + Vite + Tailwind CSS，构建后通过 `//go:embed` 打入单一 Go 二进制。
