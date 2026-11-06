# Work Tracker Server

AI Work Tracker 云端多账号、增量同步与 Gitee 发布协调服务（Go + Gin + PostgreSQL）。

## 架构特性

- **数据隔离与设备会话**：每个账号拥有独立数据空间，支持绑定桌面、手机等多台设备，会话可随时在设备端撤销。
- **增量双向同步**：基于变更序列游标（Change Sequence Cursor）与三向合并（Three-way merge），细粒度字段版本控制（`field_versions`）。
- **统一发布防重状态机**：多端直连 Gitee 与可选云端代发共享一套发布状态机与防重唯一约束。
- **轻量资源占用**：
  - 1 核 1 GB 最低规格：Go API + PostgreSQL
  - 2 核 2 GB 常规规格：支持预留 Redis 适配器与低并发队列
  - 2 核 4 GB 扩展规格：支持可选 NATS JetStream 任务投递与云端 Worker

## 接口规范

- OpenAPI 契约：[`api/openapi.yaml`](api/openapi.yaml)
- 协议详细规范：[`docs/protocol.md`](docs/protocol.md)

## 本地开发与测试

```bash
# 运行单元测试
go test ./...

# 指定 PostgreSQL 运行全量集成测试
export TEST_DATABASE_URL="postgres://user:pass@localhost:5432/postgres?sslmode=disable"
go test ./... -v
```

## 容器化部署

```bash
cd deploy
docker compose up -d
```
