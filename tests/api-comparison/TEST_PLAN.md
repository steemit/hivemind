# Hivemind API 一致性测试计划

## 概述

本文档描述如何在测试环境中验证 Go 重构版 Hivemind API 与旧版 Python API 的行为一致性。

## 测试目标

1. 验证 Go 版本 API 返回的数据结构、字段和值与 Python 版本一致
2. 确保所有 API 端点功能正常
3. 发现并修复行为差异
4. 为生产环境迁移提供信心

## 架构说明

### 系统架构

```
┌─────────────────┐     ┌─────────────────┐
│  Python 版本    │     │   Go 版本       │
│  (Legacy)       │     │  (New)          │
│  Port: 8081     │     │  Port: 8082     │
└────────┬────────┘     └────────┬────────┘
         │                        │
         └──────────┬─────────────┘
                    │
            ┌───────▼────────┐
            │  测试脚本      │
            │  (Python)      │
            │  对比结果      │
            └────────────────┘
```

### 共享数据库

两个版本应连接到同一个 PostgreSQL 数据库，以确保数据源一致。

## API 端点清单

### Hive Core API (1 个)

| API 方法 | 功能 | 优先级 |
|---------|------|--------|
| `hive.db_head_state` | 获取数据库头部状态 | P0 |

### Condenser API (23 个)

| API 方法 | 功能 | 优先级 |
|---------|------|--------|
| `condenser_api.get_followers` | 获取关注者列表 | P0 |
| `condenser_api.get_following` | 获取关注列表 | P0 |
| `condenser_api.get_follow_count` | 获取关注数 | P0 |
| `condenser_api.get_reblogged_by` | 获取转发信息 | P1 |
| `condenser_api.get_followers_by_page` | 分页获取关注者 | P1 |
| `condenser_api.get_following_by_page` | 分页获取关注 | P1 |
| `condenser_api.get_content` | 获取文章内容 | P0 |
| `condenser_api.get_content_replies` | 获取文章回复 | P0 |
| `condenser_api.get_discussions_by_trending` | 热门讨论 | P0 |
| `condenser_api.get_discussions_by_hot` | 热点讨论 | P0 |
| `condenser_api.get_discussions_by_created` | 最新讨论 | P0 |
| `condenser_api.get_discussions_by_promoted` | 推广讨论 | P1 |
| `condenser_api.get_discussions_by_blog` | 博客讨论 | P0 |
| `condenser_api.get_discussions_by_feed` | 动态讨论 | P0 |
| `condenser_api.get_blog` | 获取博客 | P0 |
| `condenser_api.get_blog_entries` | 获取博客条目 | P1 |
| `condenser_api.get_trending_tags` | 热门标签 | P1 |
| `condenser_api.get_account_reputations` | 账户声望 | P1 |
| `condenser_api.get_discussions_by_comments` | 评论讨论 | P1 |
| `condenser_api.get_replies_by_last_update` | 最新回复 | P1 |
| `condenser_api.get_discussions_by_author_before_date` | 作者历史讨论 | P1 |
| `condenser_api.get_post_discussions_by_payout` | 按收益排序文章 | P1 |
| `condenser_api.get_comment_discussions_by_payout` | 按收益排序评论 | P2 |
| `condenser_api.get_transaction` | 获取交易 | P1 |
| `condenser_api.get_state` | 获取状态 | P1 |
| `condenser_api.get_account_votes` | 获取账户投票 | P1 |

### Bridge API (19 个)

| API 方法 | 功能 | 优先级 |
|---------|------|--------|
| `bridge.get_post` | 获取文章 | P0 |
| `bridge.normalize_post` | 标准化文章 | P1 |
| `bridge.get_post_header` | 获取文章头 | P1 |
| `bridge.get_discussion` | 获取讨论 | P0 |
| `bridge.get_profile` | 获取用户资料 | P0 |
| `bridge.get_ranked_posts` | 排名文章 | P0 |
| `bridge.get_account_posts` | 账户文章 | P0 |
| `bridge.get_trending_topics` | 热门话题 | P1 |
| `bridge.get_payout_stats` | 收益统计 | P1 |
| `bridge.get_community` | 获取社区 | P1 |
| `bridge.get_community_context` | 社区上下文 | P1 |
| `bridge.list_communities` | 社区列表 | P1 |
| `bridge.list_top_communities` | 顶级社区 | P1 |
| `bridge.list_pop_communities` | 热门社区 | P1 |
| `bridge.list_community_roles` | 社区角色 | P1 |
| `bridge.list_subscribers` | 订阅者列表 | P1 |
| `bridge.list_all_subscriptions` | 所有订阅 | P2 |
| `bridge.post_notifications` | 文章通知 | P1 |
| `bridge.account_notifications` | 账户通知 | P1 |
| `bridge.unread_notifications` | 未读通知 | P1 |

### Hive API (9 个)

| API 方法 | 功能 | 优先级 |
|---------|------|--------|
| `hive_api.get_account` | 获取账户 | P0 |
| `hive_api.get_accounts` | 批量获取账户 | P0 |
| `hive_api.list_followers` | 列出关注者 | P0 |
| `hive_api.list_following` | 列出关注 | P0 |
| `hive_api.list_all_muted` | 列出所有屏蔽 | P2 |
| `hive_api.list_account_blog` | 列出账户博客 | P1 |
| `hive_api.list_account_posts` | 列出账户文章 | P1 |
| `hive_api.list_account_feed` | 列出账户动态 | P1 |

## 测试环境配置

### 环境变量

```bash
# Python 版本 API
PYTHON_API_URL=http://localhost:8081
PYTHON_API_PORT=8081

# Go 版本 API
GO_API_URL=http://localhost:8082
GO_API_PORT=8082

# 数据库（两版本共享）
DATABASE_URL=postgresql://user:pass@localhost:5432/hivemind

# 测试配置
TEST_TIMEOUT=30
TEST_PARALLEL=5
```

### Docker Compose 配置

创建 `docker-compose.test.yml`:

```yaml
version: '3.8'

services:
  postgres:
    image: postgres:15
    environment:
      POSTGRES_USER: hivemind
      POSTGRES_PASSWORD: hivemind
      POSTGRES_DB: hivemind
    ports:
      - "5432:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7
    ports:
      - "6379:6379"

  python-api:
    build:
      context: /home/ety001/workspace/hivemind_legacy
      dockerfile: Dockerfile
    ports:
      - "8081:8080"
    environment:
      DATABASE_URL: postgresql://hivemind:hivemind@postgres:5432/hivemind
      REDIS_URL: redis://redis:6379
    depends_on:
      - postgres
      - redis

  go-api:
    build:
      context: /home/ety001/workspace/hivemind
      dockerfile: Dockerfile
    ports:
      - "8082:8080"
    environment:
      HIVE_DATABASE_URL: postgresql://hivemind:hivemind@postgres:5432/hivemind
      HIVE_REDIS_URL: redis://redis:6379
      HIVE_HTTP_SERVER_PORT: 8080
    depends_on:
      - postgres
      - redis

  test-runner:
    build:
      context: ./tests/api-comparison
      dockerfile: Dockerfile
    environment:
      PYTHON_API_URL: http://python-api:8080
      GO_API_URL: http://go-api:8080
    depends_on:
      - python-api
      - go-api
    volumes:
      - ./tests/results:/app/results

volumes:
  postgres_data:
```

## 测试执行流程

### 阶段 1: 环境准备

1. 启动数据库服务
2. 启动 Python 版本 API
3. 启动 Go 版本 API
4. 验证两个版本的健康状态

### 阶段 2: P0 优先级测试

执行所有 P0 优先级的 API 测试用例

### 阶段 3: P1 优先级测试

执行所有 P1 优先级的 API 测试用例

### 阶段 4: P2 优先级测试

执行所有 P2 优先级的 API 测试用例

### 阶段 5: 报告生成

生成 HTML 和 JSON 格式的测试报告

## 数据对比策略

### 完全匹配

对于简单的标量值和列表，要求完全相等：

- 数值: `==`
- 字符串: `==`
- 布尔值: `==`
- 空值: `==`

### 近似匹配

对于浮点数和时间戳，允许一定误差：

- 浮点数: 误差 < 0.001
- 时间戳: 误差 < 1 秒

### 结构忽略

对于某些字段，可以忽略不比较：

- `request_id`
- 内部 ID
- 时间戳（有时）

### 深度比较

递归比较嵌套对象和数组，直到叶子节点

## 测试用例配置

测试用例配置文件: `tests/api-comparison/test_cases.yaml`

```yaml
# 测试用例配置
test_cases:
  - name: "获取文章内容"
    api: "condenser_api.get_content"
    priority: "P0"
    params:
      author: "steemit"
      permlink: "firstpost"
    ignore_fields:
      - "request_id"
    tolerance: {}

  - name: "获取账户信息"
    api: "hive_api.get_account"
    priority: "P0"
    params:
      account: "steemit"
    ignore_fields:
      - "last_owner_update"
    tolerance:
      timestamp_seconds: 2
```

## 失败处理

### 测试失败分类

1. **连接失败**: API 无法连接
2. **结构差异**: 返回数据结构不一致
3. **数值差异**: 返回值不一致
4. **类型差异**: 数据类型不一致
5. **超时**: 响应时间过长

### 失败报告

每个失败用例记录：
- API 方法名
- 输入参数
- 预期结果 (Python 版本)
- 实际结果 (Go 版本)
- 差异详情
- 可能原因

## 持续集成

将测试集成到 CI/CD 流程：

1. 每次代码提交自动运行测试
2. 生成测试报告并上传
3. 失败时通知开发者
4. 保留历史测试结果用于趋势分析

## 成功标准

### 阶段性目标

- **第一阶段**: P0 API 100% 通过
- **第二阶段**: P0 + P1 API 100% 通过
- **第三阶段**: 所有 API 95% 通过

### 发布标准

- 所有 P0 和 P1 API 100% 通过
- P2 API 至少 95% 通过
- 性能不低于 Python 版本的 80%
