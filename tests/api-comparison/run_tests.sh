#!/bin/bash
# API 一致性测试运行脚本

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 默认配置
PYTHON_API_URL="${PYTHON_API_URL:-http://localhost:8081}"
GO_API_URL="${GO_API_URL:-http://localhost:8082}"
TIMEOUT="${TEST_TIMEOUT:-30}"
OUTPUT_DIR="${OUTPUT_DIR:-results}"
PRIORITY="${TEST_PRIORITY:-}"
CONFIG_FILE="${CONFIG_FILE:-test_cases.yaml}"

# 解析参数
while [[ $# -gt 0 ]]; do
    case $1 in
        --python-url)
            PYTHON_API_URL="$2"
            shift 2
            ;;
        --go-url)
            GO_API_URL="$2"
            shift 2
            ;;
        --timeout)
            TIMEOUT="$2"
            shift 2
            ;;
        --output)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        --priority)
            PRIORITY="$2"
            shift 2
            ;;
        --config)
            CONFIG_FILE="$2"
            shift 2
            ;;
        --help)
            echo "用法: $0 [选项]"
            echo ""
            echo "选项:"
            echo "  --python-url URL      Python API URL (默认: http://localhost:8081)"
            echo "  --go-url URL          Go API URL (默认: http://localhost:8082)"
            echo "  --timeout SECONDS     请求超时时间(默认: 30)"
            echo "  --output DIR          结果输出目录(默认: results)"
            echo "  --priority P0/P1/P2   只运行指定优先级的测试"
            echo "  --config FILE         测试配置文件(默认: test_cases.yaml)"
            echo "  --help                显示帮助信息"
            echo ""
            echo "环境变量:"
            echo "  PYTHON_API_URL        Python API URL"
            echo "  GO_API_URL            Go API URL"
            echo "  TEST_TIMEOUT          请求超时时间"
            echo "  TEST_PRIORITY         测试优先级"
            exit 0
            ;;
        *)
            echo -e "${RED}未知选项: $1${NC}"
            exit 1
            ;;
    esac
done

# 打印配置
echo -e "${GREEN}=== Hivemind API 一致性测试 ===${NC}"
echo ""
echo "配置:"
echo "  Python API:    $PYTHON_API_URL"
echo "  Go API:        $GO_API_URL"
echo "  超时时间:      ${TIMEOUT}s"
echo "  输出目录:      $OUTPUT_DIR"
echo "  配置文件:      $CONFIG_FILE"
if [ -n "$PRIORITY" ]; then
    echo "  优先级过滤:    $PRIORITY"
fi
echo ""

# 创建输出目录
mkdir -p "$OUTPUT_DIR"

# 检查 Python
echo "检查 Python..."
if ! command -v python3 &> /dev/null; then
    echo -e "${RED}错误: 未找到 Python 3${NC}"
    exit 1
fi

# 检查依赖
echo "检查 Python 依赖..."
python3 -c "import requests, yaml" 2>/dev/null || {
    echo -e "${YELLOW}警告: 缺少依赖，正在安装...${NC}"
    pip3 install requests pyyaml -q
}

# 健康检查
echo ""
echo "检查 API 健康状态..."

check_health() {
    local url=$1
    local name=$2

    if curl -s -f "$url/health" > /dev/null 2>&1; then
        echo -e "  ${GREEN}✓${NC} $name 健康"
        return 0
    else
        echo -e "  ${RED}✗${NC} $name 不可用"
        return 1
    fi
}

# 检查两个 API
check_health "$PYTHON_API_URL" "Python API" || {
    echo -e "${RED}Python API 不可用，请检查服务是否启动${NC}"
    read -p "是否继续测试? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
}

check_health "$GO_API_URL" "Go API" || {
    echo -e "${RED}Go API 不可用，请检查服务是否启动${NC}"
    read -p "是否继续测试? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
}

# 运行测试
echo ""
echo -e "${GREEN}开始运行测试...${NC}"
echo ""

# 构建命令
CMD="python3 api_comparator.py"
CMD="$CMD --config $CONFIG_FILE"
CMD="$CMD --python-url $PYTHON_API_URL"
CMD="$CMD --go-url $GO_API_URL"
CMD="$CMD --timeout $TIMEOUT"
CMD="$CMD --output $OUTPUT_DIR"

# 执行测试
eval $CMD
EXIT_CODE=$?

# 打印结果
echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}所有测试通过!${NC}"
else
    echo -e "${RED}存在测试失败，请查看报告${NC}"
fi

exit $EXIT_CODE
