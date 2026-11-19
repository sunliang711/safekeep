# SafeKeep

SafeKeep 是一个使用 **Go** 开发的单二进制多云文件备份 HTTP 网关，内置 **React + Vite** Web 管理端（`go:embed` 打包），支持将客户请求中的文件通过同步或异步方式备份到 **S3（及兼容对象存储）** 和 **Dropbox** 等上游存储。

## 核心特性

- **全 Web 端管理**：首次启动自动引导创建管理员账号，所有配置（上游、代理、API Key、定时任务、审计日志、系统参数、迁移导入导出）均在 Web UI 完成。
- **多客户 API Key 与策略隔离**：为不同客户签发独立 API Key（数据库仅存 SHA-256 哈希），支持租户路径前缀、单文件大小上限、RPM 限流、Webhook 回调，以及“默认上游组 + 请求参数指定允许子集”。
- **每上游独立网络代理**：支持为 S3 / Dropbox 分别配置 `HTTP` / `HTTPS` / `SOCKS5` 代理（含认证账密）；Dropbox 的文件传输与 OAuth2 Token 刷新均走同一代理配置。
- **同步 / 异步双模式 + 超时转后台 + 秒传去重**：
  - `mode=sync`：并发上传后直接返回各上游路径与 ETag；超过 `sync_timeout_sec` 自动转为后台继续执行并返回 `202 Accepted` + `task_id`。
  - `mode=async`：流式落盘后立即返回 `202 Accepted`，由后台 Worker Pool 异步执行并支持失败指数退避重试。
  - **SHA-256 秒传去重**：同一客户再次上传相同哈希的文件时，若目标上游已有成功备份则直接返回成功（`deduplicated: true`），无需重复传输；同名文件自动追加时间戳与 Task ID 前缀防止覆盖。
- **审计日志与定时任务**：内置 `robfig/cron/v3` 调度器（清理过期审计日志、清理旧任务记录、缓冲盘孤儿文件回收、上游健康巡检、Dropbox Token 预刷新），支持 Web 端修改 Cron 与立即运行。
- **加密迁移导出与导入（`.skbundle`）**：支持导出含配置、凭证（临时迁移口令 Argon2id + AES-256-GCM 加密）及可选历史任务索引的迁移包，并在新环境预检冲突、重加密落库与一键恢复（保留原客户 API Key 哈希，客户端无需换 Key）。

## 快速运行

### 从源码构建单二进制

```bash
# 1. 构建前端资源到 web/dist
cd web && npm install && npm run build && cd ..

# 2. 编译 Go 单二进制
go build -o safekeep ./cmd/safekeep

# 3. 启动服务（默认监听 :8080，数据目录 ./data）
./safekeep --listen :8080 --data-dir ./data
```

启动后访问 `http://localhost:8080` 完成首次管理员初始化。

## 客户端上传 API 示例

```bash
# 同步上传到客户默认上游组
curl -X POST "http://localhost:8080/api/v1/backups?mode=sync&path=daily" \
  -H "X-API-Key: sk_your_api_key" \
  -F "file=@./backup.tar.gz"

# 异步上传到指定子集上游
curl -X POST "http://localhost:8080/api/v1/backups?mode=async&targets=prod-s3,team-dropbox" \
  -H "X-API-Key: sk_your_api_key" \
  -F "file=@./archive.zip"

# 查询任务状态与下载恢复文件
curl -H "X-API-Key: sk_your_api_key" http://localhost:8080/api/v1/backups/<task_id>
curl -H "X-API-Key: sk_your_api_key" -o restored.bin http://localhost:8080/api/v1/backups/<task_id>/download
```

## 测试

```bash
go test -race ./...
```
