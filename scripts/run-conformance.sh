#!/bin/bash
# run-conformance.sh - Run CWL conformance tests against cwl-runner
#
# Usage:
#   ./scripts/run-conformance.sh [tags]
#
# Examples:
#   ./scripts/run-conformance.sh              # Run required tests
#   ./scripts/run-conformance.sh required     # Run required tests
#   ./scripts/run-conformance.sh command_line_tool  # Run CLT tests
#   ./scripts/run-conformance.sh workflow     # Run workflow tests

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
CONFORMANCE_DIR="$PROJECT_ROOT/testdata/cwl-v1.2"
RUNNER="$PROJECT_ROOT/bin/cwl-runner"
BADGE_DIR="$PROJECT_ROOT/badges"
TAGS="${1:-required}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=== CWL v1.2 Conformance Tests ==="
echo ""

# Check if cwltest is installed
if ! command -v cwltest &> /dev/null; then
    echo -e "${YELLOW}cwltest not found. Installing...${NC}"
    pip install cwltest
fi

# Build cwl-runner
echo "Building cwl-runner..."
cd "$PROJECT_ROOT"
go build -o bin/cwl-runner ./cmd/cwl-runner
echo -e "${GREEN}Built bin/cwl-runner${NC}"

# Clone conformance tests if not present
if [ ! -d "$CONFORMANCE_DIR" ]; then
    echo "Cloning CWL v1.2 conformance tests..."
    git clone --depth 1 https://github.com/common-workflow-language/cwl-v1.2.git "$CONFORMANCE_DIR"
fi

# Create badge directory
mkdir -p "$BADGE_DIR"

# Run conformance tests
echo ""
echo "Running conformance tests with tags: $TAGS"
echo ""

cd "$CONFORMANCE_DIR"

cwltest \
    --test conformance_tests.yaml \
    --tool "$RUNNER" \
    --tags "$TAGS" \
    --badgedir "$BADGE_DIR" \
    --verbose \
    2>&1 | tee "$PROJECT_ROOT/conformance-results.txt"

# Check result
if [ ${PIPESTATUS[0]} -eq 0 ]; then
    echo ""
    echo -e "${GREEN}=== All $TAGS tests passed! ===${NC}"
else
    echo ""
    echo -e "${RED}=== Some tests failed ===${NC}"
    exit 1
fi
