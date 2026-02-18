# Hivemind API 一致性测试工具

用于测试 Go 重构版 Hivemind API 与旧版 Python API 的响应一致性。

## 目录结构

```
tests/api-comparison/
├── api_comparator.py      # 核心测试脚本
├── run_tests.sh           # 测试运行脚本
├── test_cases.yaml        # 测试用例配置
├── requirements.txt       # Python 依赖
├── Dockerfile            # Docker 镜像
├── docker-compose.test.yml # Docker Compose 配置
├── TEST_PLAN.md          # 测试计划文档
├── API_ENDPOINTS.md      # API 端点清单
└── results/              # 测试结果输出目录
```

## 快速开始

### 1. 安装依赖

```bash
cd tests/api-comparison
pip install -r requirements.txt
```

### 2. 启动 API 服务

确保 Python 版本和 Go 版本的 API 服务都已启动：

```bash
# Python 版本 (端口 8081)
cd /home/ety001/workspace/hivemind_legacy
python -m hive.server.condenser_api

# Go 版本 (端口 8082)
cd /home/ety001/workspace/hivemind
make run-server
```

### 3. 运行测试

```bash
# 给脚本执行权限
chmod +x run_tests.sh

# 运行测试
./run_tests.sh
```

### 4. 查看结果

测试结果将保存在 `results/` 目录：

- `report-*.json` - JSON 格式报告
- `report-*.html` - HTML 格式报告

## 使用 Docker

### 使用 Docker Compose

```bash
# 构建并启动所有服务
docker-compose -f docker-compose.test.yml up -d

# 查看日志
docker-compose -f docker-compose.test.yml logs -f test-runner

# 停止服务
docker-compose -f docker-compose.test.yml down
```

### 单独运行测试容器

```bash
# 构建镜像
docker build -t hivemind-api-test .

# 运行测试
docker run --rm \
  -e PYTHON_API_URL=http://host.docker.internal:8081 \
  -e GO_API_URL=http://host.docker.internal:8082 \
  -v $(pwd)/results:/app/results \
  hivemind-api-test
```

## 命令行选项

```bash
python api_comparator.py [选项]

选项:
  --config FILE          测试配置文件 (默认: test_cases.yaml)
  --python-url URL       Python API URL (默认: http://localhost:8081)
  --go-url URL           Go API URL (默认: http://localhost:8082)
  --timeout SECONDS      请求超时时间 (默认: 30)
  --output DIR           结果输出目录 (默认: results)
  --no-html              不生成 HTML 报告
```

## 环境变量

```bash
# API 地址
export PYTHON_API_URL=http://localhost:8081
export GO_API_URL=http://localhost:8082

# 测试配置
export TEST_TIMEOUT=30
export TEST_PRIORITY=P0  # 只运行 P0 优先级测试

# 输出配置
export OUTPUT_DIR=results
```

## 测试用例配置

编辑 `test_cases.yaml` 文件来添加或修改测试用例：

```yaml
test_cases:
  - name: "获取文章内容"
    api: "condenser_api.get_content"
    priority: "P0"
    params:
      author: "steemit"
      permlink: "firstpost"
    ignore_fields:
      - "request_id"
    tolerance:
      timestamp_seconds: 2
```

## 配置说明

### ignore_fields

指定要忽略的字段，这些字段不会参与比较：

```yaml
ignore_fields:
  - "request_id"
  - "id"
```

### tolerance

指定容差配置：

```yaml
tolerance:
  float_epsilon: 0.001      # 浮点数误差
  timestamp_seconds: 2      # 时间戳误差(秒)
```

## 优先级分类

- **P0**: 核心功能，必须通过
- **P1**: 重要功能，应该通过
- **P2**: 次要功能，可以容忍少量失败

## 故障排查

### 连接失败

```
错误: 请求失败: HTTPConnectionPool
```

解决方案：
1. 检查 API 服务是否启动
2. 检查端口是否正确
3. 检查防火墙设置

### 数据不一致

```
错误: 值不匹配: field - Python: 100, Go: 101
```

解决方案：
1. 检查数据库状态
2. 检查 API 版本
3. 使用 `ignore_fields` 或 `tolerance` 调整比较规则

### 超时

```
错误: 请求失败: Read timeout
```

解决方案：
1. 增加 `--timeout` 参数
2. 检查 API 服务性能
3. 检查网络连接

## 持续集成

将测试集成到 CI/CD 流程：

```yaml
# .github/workflows/api-test.yml
name: API Consistency Test

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v2
      - name: Run API tests
        run: |
          cd tests/api-comparison
          ./run_tests.sh --output results
      - name: Upload results
        uses: actions/upload-artifact@v2
        with:
          name: test-results
          path: tests/api-comparison/results/
```

## 贡献

添加新的测试用例：

1. 编辑 `test_cases.yaml`
2. 添加测试用例配置
3. 运行测试验证
4. 提交更改

## 许可

MIT License
