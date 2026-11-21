# 仪表盘 API 上传分布饼图

## 任务背景与统计口径

在概览仪表盘增加按今日、本周、本月切换的饼图，展示各 API 客户成功受理的备份文件数、总数和占比。

- 主体以 API Key 所属客户 ID 区分，名称优先使用当前客户名称，客户删除后保留审计名称。
- 统计 `backup.upload` 的客户成功审计记录；同步返回失败的任务不计入。
- 包含秒传去重和已受理的异步任务，排除网页上传、失败请求、幂等重放；相同任务 ID 去重计数。
- 采用服务端本地时区，今日从零点、本周从周一零点、本月从一号零点开始，统计至本次查询时刻。

## 实现与文件变更

- `internal/db/dashboard_uploads.go`：统计 DTO、日历边界计算和 SQL 聚合，返回按上传数降序排列的客户列表。
- `internal/db/models.go`、`internal/db/store_tasks.go`：扩展现有仪表盘摘要，附加 `api_uploads` 字段。
- `web/src/types.ts`：补充统计响应类型。
- `web/src/components/APIUploadsChart.tsx`：SVG 饼图、时间切换、数量与占比图例、空状态和时区说明。
- `web/src/pages/DashboardPage.tsx`：接入新图表。
- `web/src/i18n/zh.ts`、`web/src/i18n/en.ts`：补充中英文文案。
- `internal/db/dashboard_uploads_test.go`、`internal/server/dashboard_test.go`：时间边界、筛选规则、客户身份和接口鉴权测试。

## 接口与配置

沿用管理员鉴权接口 `GET /admin/api/v1/dashboard/summary`，新增字段示例：

```json
{
  "api_uploads": {
    "today": [{"client_id": "client-a", "client_name": "订单服务", "upload_count": 4}],
    "week": [{"client_id": "client-a", "client_name": "订单服务", "upload_count": 7}],
    "month": [{"client_id": "client-a", "client_name": "订单服务", "upload_count": 7}],
    "timezone": "+08 (UTC+08:00)"
  }
}
```

三个时间范围一次返回，前端切换无需额外请求；空列表返回 `[]`。查询失败沿用仪表盘现有错误响应。无需新增接口、表结构、配置项或依赖。

## 验证

- 前端 TypeScript 检查及 Vite 生产构建。
- `go vet ./...`。
- `go test -count=1 -race ./...`。
- 覆盖零点、周一、周日、跨月、跨年、夏令时、未来记录排除，以及重复任务、幂等重放、去重、异步受理、失败请求和网页上传的计数规则。
- 覆盖客户改名、客户删除、同名客户、空数组响应和管理员接口鉴权。
- 使用独立临时数据库验证浏览器中的空状态、三档切换、数量与占比、配色一致性、明暗主题、中英文及移动布局。

## 部署与限制

按原有流程重新构建后端和嵌入的前端资源即可发布，无需执行数据迁移。统计依赖当前保留的审计日志，手动删除、清理或未导入的日志不会计入；异步任务后续执行结果不改变已成功受理的文件数。

## [v0.0.14] - 2026-11-21

### Added

- 仪表盘新增 API 客户上传备份文件分布饼图，支持今日、本周、本月切换并显示数量与占比。
