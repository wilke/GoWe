#!/bin/bash
#
# test-distributed-pipeline.sh - Run the 3-step distributed test pipeline
#
# This script demonstrates:
# 1. Running a multi-step workflow on distributed workers
# 2. Accessing output files via the shared volume
#
# The shared volume (./tmp/workdir) is mounted on both the server and workers,
# allowing output files to be accessed from the host machine.
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

cd "$PROJECT_DIR"

# Colors
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_header() { echo -e "\n${CYAN}=== $1 ===${NC}\n"; }

PORT=8090
SERVER_URL="http://localhost:${PORT}"

log_header "Distributed Pipeline Test"

# Check if docker-compose is running
if ! docker-compose ps 2>/dev/null | grep -q "gowe-server"; then
    log_info "Starting docker-compose environment..."
    docker-compose up -d
    sleep 5

    # Wait for server health
    log_info "Waiting for server..."
    for i in {1..30}; do
        if curl -sf "${SERVER_URL}/api/v1/health" > /dev/null 2>&1; then
            break
        fi
        sleep 1
    done

    # Wait for workers
    log_info "Waiting for workers..."
    sleep 5
fi

# Check workers
WORKER_COUNT=$(curl -sf "${SERVER_URL}/api/v1/workers" | jq '.data | length')
log_info "Workers registered: ${WORKER_COUNT}"

# Build CLI if needed
if [ ! -f bin/gowe ]; then
    log_info "Building gowe CLI..."
    go build -o bin/gowe ./cmd/cli
fi

log_header "Running Pipeline"

# Create output directory
mkdir -p tmp/workdir/outputs

# Run the pipeline
log_info "Submitting workflow..."
cd testdata/distributed-test

RESULT=$(../../bin/gowe --server "${SERVER_URL}" run pipeline.cwl job.yml 2>&1)
EXIT_CODE=$?

cd "$PROJECT_DIR"

if [ $EXIT_CODE -eq 0 ]; then
    log_info "Pipeline completed successfully!"
    echo ""
    echo "Workflow output:"
    echo "$RESULT" | jq '.'
else
    echo -e "${YELLOW}Pipeline failed:${NC}"
    echo "$RESULT"
    exit 1
fi

log_header "Accessing Output Files"

echo "Output files are stored in the shared volume: ./tmp/workdir/outputs/"
echo ""
echo "Directory contents:"
find tmp/workdir/outputs -type f -name "*.txt" 2>/dev/null | head -20 || echo "  (no .txt files found yet)"

echo ""
log_info "To view a specific output file:"
echo "  cat tmp/workdir/outputs/<task_id>/numbers.txt"
echo "  cat tmp/workdir/outputs/<task_id>/line_count.txt"
echo "  cat tmp/workdir/outputs/<task_id>/exists_result.txt"

# Try to find and display the actual files
echo ""
log_header "Output File Contents"

NUMBERS_FILE=$(find tmp/workdir/outputs -name "numbers.txt" 2>/dev/null | head -1)
if [ -n "$NUMBERS_FILE" ]; then
    log_info "numbers.txt (first 10 lines):"
    head -10 "$NUMBERS_FILE"
    echo "  ... ($(wc -l < "$NUMBERS_FILE" | tr -d ' ') total lines)"
fi

COUNT_FILE=$(find tmp/workdir/outputs -name "line_count.txt" 2>/dev/null | head -1)
if [ -n "$COUNT_FILE" ]; then
    echo ""
    log_info "line_count.txt:"
    cat "$COUNT_FILE"
fi

EXISTS_FILE=$(find tmp/workdir/outputs -name "exists_result.txt" 2>/dev/null | head -1)
if [ -n "$EXISTS_FILE" ]; then
    echo ""
    log_info "exists_result.txt:"
    cat "$EXISTS_FILE"
fi

log_header "How to Access Results"

cat << 'EXPLANATION'
The distributed workers write output files to a shared volume that is
mounted on both the Docker containers and your host machine:

  Host path:      ./tmp/workdir/outputs/
  Container path: /workdir/outputs/

Each task's outputs are stored in a subdirectory named after the task ID:
  ./tmp/workdir/outputs/<task_id>/<output_files>

The workflow JSON output contains file paths like:
  "location": "file:///workdir/outputs/task_xxx/numbers.txt"

To access from host, replace /workdir with ./tmp/workdir:
  ./tmp/workdir/outputs/task_xxx/numbers.txt

You can also use the GoWe API to get submission details:
  curl http://localhost:8090/api/v1/submissions/<sub_id>

Or list all output files:
  find ./tmp/workdir/outputs -type f -name "*.txt"
EXPLANATION

echo ""
log_info "Test complete!"
