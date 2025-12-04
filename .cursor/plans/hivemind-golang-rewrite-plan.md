---
name: Hivemind 代码分析与 Golang 重写计划
overview: 分析 legacy 目录下的所有 Python 代码，生成详细的接口和业务逻辑文档，并制定使用 Golang + Gin + OpenTelemetry + Jaeger + Prometheus 的完整重写计划。
todos:
  - id: analyze-codebase
    content: 分析 legacy 目录下的所有代码，梳理模块结构和依赖关系
    status: pending
  - id: generate-api-docs
    content: 生成详细的 API 接口文档，包含所有 condenser_api、bridge_api、hive_api 的方法
    status: pending
  - id: generate-business-logic-docs
    content: 生成业务逻辑文档，详细说明索引器、缓存、社区等模块的工作流程
    status: pending
  - id: generate-db-schema-docs
    content: 生成数据库架构文档，包含所有表结构、索引、关系说明
    status: pending
  - id: generate-architecture-docs
    content: 生成系统架构文档，说明整体设计和工作原理
    status: pending
  - id: create-golang-project-structure
    content: 创建 Golang 项目目录结构，初始化 go.mod 和基础配置
    status: pending
  - id: setup-telemetry
    content: 集成 OpenTelemetry、Jaeger 和 Prometheus
    status: pending
  - id: implement-db-layer
    content: 实现数据库访问层，包括模型定义和 Repository pattern
    status: pending
  - id: implement-indexer-core
    content: 实现索引器核心功能：区块同步、账户索引、帖子索引、关注关系索引
    status: pending
  - id: implement-condenser-api
    content: 实现 condenser_api 的所有接口
    status: pending
  - id: implement-bridge-api
    content: 实现 bridge_api 的所有接口
    status: pending
  - id: implement-hive-api
    content: 实现 hive_api 的所有接口
    status: pending
  - id: implement-community-features
    content: 实现完整的社区功能（角色管理、订阅、帖子管理等）
    status: pending
  - id: implement-notification-system
    content: 实现通知系统
    status: pending
  - id: optimize-and-test
    content: 性能优化、测试和部署准备
    status: pending
---

# Hivemind 代码分析与 Golang 重写计划

## 阶段一：代码分析与文档生成

### 1.1 项目结构分析

- 分析 `legacy/hive/` 目录下的所有模块
- 梳理模块依赖关系
- 识别核心业务逻辑组件

### 1.2 API 接口文档

生成详细的 API 文档，包含：

- **condenser_api**: 兼容 steemd 的 API 接口（约 20+ 个方法）
  - Follow 相关：`get_followers`, `get_following`, `get_follow_count`, `get_reblogged_by`
  - Content 相关：`get_content`, `get_content_replies`, `get_state`
  - Discussion 相关：`get_discussions_by_trending`, `get_discussions_by_hot`, `get_discussions_by_created`, `get_discussions_by_blog`, `get_discussions_by_feed` 等
  - Blog 相关：`get_blog`, `get_blog_entries`
  - Tags 相关：`get_trending_tags`
  - 其他：`get_account_reputations`, `get_transaction`
- **bridge_api**: 桥接 API（约 10+ 个方法）
  - `get_post`, `get_profile`, `get_ranked_posts`, `get_account_posts`, `get_trending_topics`
  - `normalize_post`, `get_post_header`, `get_discussion`
- **hive_api**: Hive 专用 API
  - Community: `get_community`, `list_communities`, `list_subscribers` 等
  - Notify: `post_notifications`, `account_notifications`, `unread_notifications`
  - Public: `get_account`, `list_followers`, `list_account_blog` 等
  - Stats: `get_payout_stats`
- 每个接口包含：方法签名、参数说明、返回值结构、示例

### 1.3 业务逻辑文档

- **索引器（Indexer）模块**：
  - `sync.py`: 同步管理器，处理初始同步、快速同步、实时监听
  - `blocks.py`: 区块处理
  - `posts.py`: 帖子管理（创建、编辑、删除、恢复）
  - `accounts.py`: 账户管理
  - `follow.py`: 关注关系处理
  - `community.py`: 社区操作（创建、角色管理、订阅、帖子管理）
  - `cached_post.py`: 帖子缓存管理
  - `feed_cache.py`: Feed 缓存
  - `notify.py`: 通知系统
  - `payments.py`: 支付处理
  - `custom_op.py`: 自定义操作处理
- **数据库架构**：
  - 所有表结构（`hive_blocks`, `hive_accounts`, `hive_posts`, `hive_follows`, `hive_communities`, `hive_notifs` 等）
  - 索引设计
  - 关系说明
- **Steem 链交互**：
  - 区块流处理
  - 账户数据获取
  - 内容获取
  - 动态全局属性

### 1.4 文档输出

在 `docs/` 目录下生成：

- `api-reference.md`: 完整的 API 参考文档
- `business-logic.md`: 业务逻辑详细说明
- `database-schema.md`: 数据库架构文档
- `architecture.md`: 系统架构说明
- `indexer-flow.md`: 索引器工作流程

## 阶段二：Golang 重写计划

### 2.1 项目结构设计

```
hivemind/
├── cmd/
│   ├── server/          # API 服务器
│   └── indexer/         # 索引器服务
├── internal/
│   ├── api/            # API 层
│   │   ├── condenser/  # condenser_api
│   │   ├── bridge/      # bridge_api
│   │   └── hive/       # hive_api
│   ├── indexer/         # 索引器逻辑
│   ├── db/             # 数据库访问层
│   ├── steem/          # Steem SDK 封装
│   ├── cache/          # 缓存层（Redis）
│   ├── models/         # 数据模型
│   └── utils/          # 工具函数
├── pkg/
│   ├── telemetry/      # OpenTelemetry 集成
│   └── config/         # 配置管理
├── migrations/         # 数据库迁移
├── docs/              # 文档（从阶段一生成）
└── legacy/            # 原有 Python 代码
```

### 2.2 技术栈

- **Web 框架**: Gin
- **数据库**: PostgreSQL (使用 `github.com/lib/pq` 或 `gorm.io/gorm`)
- **缓存**: Redis (使用 `github.com/go-redis/redis/v8`)
- **Steem SDK**: `github.com/steemit/steemgosdk`
- **可观测性**:
  - OpenTelemetry: `go.opentelemetry.io/otel`
  - Jaeger: `go.opentelemetry.io/otel/exporters/jaeger`
  - Prometheus: `github.com/prometheus/client_golang`
- **配置**: Viper (`github.com/spf13/viper`)
- **日志**: Zap (`go.uber.org/zap`) + Scalyr 格式支持

### 2.3 核心模块实现顺序

#### Phase 1: 基础设施

1. 项目初始化（go.mod, 目录结构）
2. 配置管理（环境变量、配置文件）
3. 数据库连接与迁移
4. Redis 连接
5. OpenTelemetry 集成（Jaeger + Prometheus）
6. 日志系统
   - 集成 Zap logger
   - 实现 Scalyr 兼容的 JSON 格式 encoder
   - 配置日志字段结构（包含 trace_id, span_id 等）
   - 测试日志在 Scalyr 中的展示效果
7. Steem SDK 封装
8. **分叉处理策略评估**
   - 分析旧版本分叉处理逻辑
   - 评估方案A（追最新块+回滚）的可行性
   - 如果不可行，采用方案B（仅追不可逆块）

#### Phase 2: 数据模型与数据库层

1. 定义所有数据模型（GORM models）
2. 数据库访问层（Repository pattern）
3. 数据库迁移脚本
4. 连接池配置

#### Phase 3: 索引器核心

1. 区块同步管理器
   - 根据分叉处理策略评估结果实现：
     - **方案A**: 区块队列、分叉检测、有限度回滚（最多25块）
     - **方案B**: 仅同步到不可逆块高度
   - 启动时的分叉验证（`verify_head` 逻辑）
2. 区块处理逻辑
3. 账户索引
4. 帖子索引（创建、编辑、删除）
5. 关注关系索引
6. Feed 缓存构建
7. 帖子缓存管理

#### Phase 4: API 层 - Condenser API

1. Follow 相关接口
2. Content 相关接口
3. Discussion 查询接口（trending, hot, created, blog, feed）
4. Tags 相关接口
5. Blog 相关接口
6. 其他辅助接口

#### Phase 5: API 层 - Bridge API

1. Post 相关接口
2. Profile 接口
3. Ranked posts 接口
4. Account posts 接口
5. Trending topics 接口

#### Phase 6: API 层 - Hive API

1. Community 相关接口
2. Notification 接口
3. Public API 接口
4. Stats 接口

#### Phase 7: 高级功能

1. 社区功能完整实现
2. 通知系统
3. 支付统计
4. 缓存优化
5. 性能优化

#### Phase 8: 测试与部署

1. 单元测试
2. 集成测试
3. API 测试
4. Docker 化
5. 部署文档

### 2.4 关键设计决策

1. **并发模型**: 使用 goroutine 池处理区块同步，channel 进行通信
2. **数据库事务**: 每个区块处理使用事务，确保数据一致性
3. **缓存策略**: 
   - Redis 缓存热点数据（帖子内容、账户信息）
   - 缓存失效策略
4. **可观测性**:
   - 所有 API 请求添加 OpenTelemetry span
   - Prometheus metrics: 请求数、延迟、错误率、区块同步速度
   - Jaeger 分布式追踪
5. **错误处理**: 统一的错误处理中间件
6. **API 兼容性**: 保持与 Python 版本的 JSON-RPC 接口完全兼容
7. **日志格式**: 
   - 使用结构化日志（JSON 格式）便于 Scalyr 解析
   - 日志字段包含：timestamp, level, service, component, message, trace_id, span_id, 以及业务相关字段
   - 参考 Scalyr 的日志展示模板，确保关键字段可被正确索引和查询
   - 实现自定义 Zap encoder 输出 Scalyr 兼容格式
8. **分叉处理策略**:
   - **评估阶段**: 深入分析旧版本的分叉处理逻辑（`BlockQueue`, `verify_head`, `_pop` 方法）
   - **方案A（如果可行）**: 实现追最新块 + 有限度回滚（最多25个区块）
     - 实现区块队列和分叉检测
     - 实现分叉回滚逻辑（删除受影响的数据）
     - 处理 MicroFork 和 Fork 异常
     - 风险：数据一致性、回滚复杂度、性能影响
   - **方案B（如果方案A不可行）**: 仅追踪不可逆区块高度
     - 更简单、更稳定
     - 延迟约 21 秒（Steem 不可逆块确认时间）
     - 无需处理分叉回滚
   - **决策标准**: 
     - 方案A的稳定性评估（数据一致性风险）
     - 实现复杂度评估（回滚逻辑的完整性）
     - 性能影响评估
     - 如果评估结果不理想，采用方案B

### 2.5 性能目标

- API 响应时间: P95 < 200ms
- 区块同步: 跟上链的实时速度（约 3 秒一个区块）
- 并发处理: 支持 1000+ 并发请求
- 数据库查询优化: 使用适当的索引和查询优化

### 2.6 迁移策略

1. 保持数据库 schema 兼容（使用相同的表结构）
2. 可以并行运行 Python 和 Go 版本进行验证
3. 逐步切换流量
4. 数据一致性检查工具

## 实施步骤

1. **第一步**: 完成代码分析，生成所有文档（预计 2-3 天）
2. **第二步**: 搭建 Go 项目基础架构（1-2 天）
   - 包含 Scalyr 日志格式实现
   - 分叉处理策略评估
3. **第三步**: 实现索引器核心功能（1-2 周）
   - 根据评估结果实现相应的同步策略
4. **第四步**: 实现 API 层（2-3 周）
5. **第五步**: 实现高级功能（1-2 周）
6. **第六步**: 测试与优化（1-2 周）

## 文档输出位置

所有文档将保存在 `docs/` 目录下：

- `api-reference.md`
- `business-logic.md`
- `database-schema.md`
- `architecture.md`
- `indexer-flow.md`
- `golang-rewrite-plan.md` (本计划)

