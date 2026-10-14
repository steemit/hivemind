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

### Condenser API (26 个)

| API 方法 | 功能 | 优先级 |
|---------|------|--------|
| `condenser_api.get_followers` | 获取关注者列表 | P0 |
| `condenser_api.get_following` | 获取关注列表 | P0 |
| `condenser_api.get_follow_count` | 获取关注数 | P0 |
| `condenser_api.get_reblogged_by` | 获取转发信息 | P0 |
| `condenser_api.get_followers_by_page` | 分页获取关注者 | P1 |
| `condenser_api.get_following_by_page` | 分页获取关注 | P1 |
| `condenser_api.get_content` | 获取文章内容 | P0 |
| `condenser_api.get_content_replies` | 获取文章回复 | P1 |
| `condenser_api.get_discussions_by_trending` | 热门讨论 | P0 |
| `condenser_api.get_discussions_by_hot` | 热点讨论 | P0 |
| `condenser_api.get_discussions_by_created` | 最新讨论 | P0 |
| `condenser_api.get_discussions_by_promoted` | 推广讨论 | P0 |
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
| `condenser_api.get_transaction` | 获取交易 | P2 |
| `condenser_api.get_state` | 获取状态 | P1 |
| `condenser_api.get_account_votes` | 获取账户投票 | P2 |

### Bridge API (20 个)

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
| `bridge.get_community` | 获取社区 | P0 |
| `bridge.get_community_context` | 社区上下文 | P1 |
| `bridge.list_communities` | 社区列表 | P0 |
| `bridge.list_top_communities` | 顶级社区 | P2 |
| `bridge.list_pop_communities` | 热门社区 | P1 |
| `bridge.list_community_roles` | 社区角色 | P1 |
| `bridge.list_subscribers` | 订阅者列表 | P1 |
| `bridge.list_all_subscriptions` | 所有订阅 | P2 |
| `bridge.post_notifications` | 文章通知 | P1 |
| `bridge.account_notifications` | 账户通知 | P0 |
| `bridge.unread_notifications` | 未读通知 | P0 |

### Hive API (8 个)

| API 方法 | 功能 | 优先级 |
|---------|------|--------|
| `hive_api.get_account` | 获取账户 | P2 |
| `hive_api.get_accounts` | 批量获取账户 | P2 |
| `hive_api.list_followers` | 列出关注者 | P2 |
| `hive_api.list_following` | 列出关注 | P2 |
| `hive_api.list_all_muted` | 列出所有屏蔽 | P2 |
| `hive_api.list_account_blog` | 列出账户博客 | P2 |
| `hive_api.list_account_posts` | 列出账户文章 | P2 |
| `hive_api.list_account_feed` | 列出账户动态 | P2 |

> 优先级与 `test_cases.yaml` 对齐（2026-Q4 矩阵口径）：P0=22 / P1=10 /
> P2=11。Go-only 方法（`hive_api.*`、`bridge.list_top_communities`）在
> cross-service 对比中必然红（legacy 无此方法，决策 D4），统一标 P2，
> 待 #421 重构矩阵后移入 Go-vs-Go 自检轨道。

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

对于数值、时间戳和金额，允许一定误差（口径见 2026-Q4/04-comparison.md §五）：

- 数值: `float_epsilon`（默认 0.001）
- 时间戳: 归一为 epoch 后按 `timestamp_seconds`（默认 2 秒）比较；
  识别 ISO8601 变体（带/不带 Z、带偏移、无时区、空格分隔、日期-only）
  与 epoch 秒/毫秒（数值仅在时间语义字段名上生效，如 `created`、`db_head_age`）
- 金额字符串: `parse_amount` 归一为数值后按 `float_epsilon` 比较
  （"1.500 SBD" 与 "1.5 SBD" 判等）；**币种必须匹配**（大小写不敏感；
  "$" 只匹配 "$"；SBD vs STEEM 判红）；**金额串不与裸数字桥接**
  （"0.750 SBD" vs 0.75 判 type mismatch——数字侧无币种可校验）

### 结构忽略（路径感知）

`ignore_fields` 为路径感知匹配（见 `api_comparator.py` 的 `IgnoreMatcher`）：

- 裸字段名（如 `request_id`）匹配任意深度的同名键（保留 legacy 深度语义，慎用）
- 点分路径（如 `author.id`）只匹配该精确路径，防止全局 `id` 误伤所有层级的业务字段
- 支持 `*`（任一段）与 `**`（任意层级段）；list 下标归一为 `*`

### 深度比较

递归比较嵌套对象和数组，直到叶子节点；list 默认严格按位置比较，
per-case 可选 `unordered`（按业务键排序后比）与 `lenient_list`
（长度 + 集合式一对一匹配），仅用于 legacy 本身无序的场景并在 yaml 注释登记理由。

## 测试用例配置

测试用例配置文件: `tests/api-comparison/test_cases.yaml`

参数形态以 legacy Python 端协议为准：condenser 用例为**位置数组**
（讨论类查询为 `[query对象]` 嵌套形态）；bridge/hive_api 用例为对象
（键名与 legacy 方法签名一致）。

```yaml
# 顶层全局配置（逐层应用到每个用例，per-case 可覆盖）
config:
  ignore_fields: []            # 路径感知
  tolerance:
    float_epsilon: 0.001
    timestamp_seconds: 2

test_cases:
  - id: "condenser-get-content"          # 稳定 id，用于结果文件命名
    name: "condenser_api.get_content steemit/firstpost"
    api: "condenser_api.get_content"
    priority: "P0"
    params: ["steemit", "firstpost"]     # 位置数组（legacy 协议形态）
    # ignore_fields: [...]               # per-case 覆盖（整体替换全局）
    # tolerance: {...}                   # per-case 覆盖（按键合并）
    # unordered: true                    # 顶层 list 按业务键排序后比
    # unordered_key: "author"
    # lenient_list: true                 # 顶层 list 长度 + 集合式匹配
```

工具自测（不依赖真实服务）：

```bash
cd tests/api-comparison
python3 -m pytest test_api_comparator.py    # 比较器核心单元测试
python3 selftest/run_selftest.py            # stub 双端端到端冒烟
```

### 退出码约定

`api_comparator.py` 与 `run_tests.sh` 退出码一致：

- `0` = 所有执行的用例全部通过
- `1` = 至少一个用例失败
- `2` = 没有用例被执行（如 `--priority` 过滤后为空——零证据不得
  视为"全绿"，CI 中必须与 0 区分）

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
