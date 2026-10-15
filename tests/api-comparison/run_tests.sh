#!/bin/bash
# Hivemind API consistency test runner.
#
# Wraps api_comparator.py: checks dependencies and endpoint health, then
# runs the comparison suite and preserves the comparator exit code.

set -e

# Self-anchor to this script's directory (2026-Q4 04 §一 1.8): the tool used
# to resolve api_comparator.py/test_cases.yaml relative to the CALLER's cwd,
# so `bash /path/to/tests/api-comparison/run_tests.sh` from anywhere else
# broke. All relative paths below now resolve against the tool directory.
cd "$(dirname "$0")"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Defaults (overridable via environment and flags)
PYTHON_API_URL="${PYTHON_API_URL:-http://localhost:8081}"
GO_API_URL="${GO_API_URL:-http://localhost:8082}"
TIMEOUT="${TEST_TIMEOUT:-30}"
OUTPUT_DIR="${OUTPUT_DIR:-results}"
PRIORITY="${TEST_PRIORITY:-}"
CONFIG_FILE="${CONFIG_FILE:-test_cases.yaml}"

# Parse arguments
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
            echo "Usage: $0 [options]"
            echo ""
            echo "Options:"
            echo "  --python-url URL      Python API URL (default: http://localhost:8081)"
            echo "  --go-url URL          Go API URL (default: http://localhost:8082)"
            echo "  --timeout SECONDS     Request timeout (default: 30)"
            echo "  --output DIR          Result output directory (default: results)"
            echo "  --priority P0,P1,...  Only run cases with these priorities"
            echo "  --config FILE         Test case config file (default: test_cases.yaml)"
            echo "  --help                Show this help"
            echo ""
            echo "Environment variables:"
            echo "  PYTHON_API_URL        Python API URL"
            echo "  GO_API_URL            Go API URL"
            echo "  TEST_TIMEOUT          Request timeout"
            echo "  TEST_PRIORITY         Priority filter (same format as --priority)"
            echo "  OUTPUT_DIR            Result output directory"
            echo "  CONFIG_FILE           Test case config file"
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            exit 1
            ;;
    esac
done

# Print configuration
echo -e "${GREEN}=== Hivemind API consistency test ===${NC}"
echo ""
echo "Config:"
echo "  Python API:    $PYTHON_API_URL"
echo "  Go API:        $GO_API_URL"
echo "  Timeout:       ${TIMEOUT}s"
echo "  Output dir:    $OUTPUT_DIR"
echo "  Config file:   $CONFIG_FILE"
if [ -n "$PRIORITY" ]; then
    echo "  Priority:      $PRIORITY"
fi
echo ""

# Create the output directory
mkdir -p "$OUTPUT_DIR"

# Check Python
echo "Checking Python..."
if ! command -v python3 &> /dev/null; then
    echo -e "${RED}Error: python3 not found${NC}"
    exit 1
fi

# Check dependencies
echo "Checking Python dependencies..."
python3 -c "import requests, yaml" 2>/dev/null || {
    echo -e "${YELLOW}Warning: missing dependencies, installing...${NC}"
    pip3 install requests pyyaml -q
}

# Health check
echo ""
echo "Checking API health..."

check_health() {
    local url=$1
    local name=$2

    if curl -s -f "$url/health" > /dev/null 2>&1; then
        echo -e "  ${GREEN}OK${NC} $name healthy"
        return 0
    else
        echo -e "  ${RED}FAIL${NC} $name unreachable"
        return 1
    fi
}

# Check both APIs
check_health "$PYTHON_API_URL" "Python API" || {
    echo -e "${RED}Python API unreachable; check that the server is running${NC}"
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
}

check_health "$GO_API_URL" "Go API" || {
    echo -e "${RED}Go API unreachable; check that the server is running${NC}"
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
}

# Run the tests
echo ""
echo -e "${GREEN}Running tests...${NC}"
echo ""

# Build the comparator command (array form: no eval, no word splitting)
CMD=(python3 api_comparator.py)
CMD+=("--config" "$CONFIG_FILE")
CMD+=("--python-url" "$PYTHON_API_URL")
CMD+=("--go-url" "$GO_API_URL")
CMD+=("--timeout" "$TIMEOUT")
CMD+=("--output" "$OUTPUT_DIR")

# Pass the priority filter through to the comparator (P0/P1/P2, comma separated)
if [ -n "$PRIORITY" ]; then
    CMD+=("--priority" "$PRIORITY")
fi

# Run the tests; keep the exit code even on failure (set -e would abort here)
set +e
"${CMD[@]}"
EXIT_CODE=$?
set -e

# Print the outcome
# Exit-code contract: 0 = all executed cases passed, 1 = at least one
# failure, 2 = no cases executed (empty run, e.g. a --priority filter that
# matched nothing -- zero evidence must not read as "all green" in CI).
echo ""
if [ $EXIT_CODE -eq 0 ]; then
    echo -e "${GREEN}All tests passed!${NC}"
elif [ $EXIT_CODE -eq 2 ]; then
    echo -e "${RED}No test cases executed (empty run) -- check the priority filter '${PRIORITY}'.${NC}"
else
    echo -e "${RED}Some tests failed; see the reports in $OUTPUT_DIR${NC}"
fi

exit $EXIT_CODE
