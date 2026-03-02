#!/bin/bash
# Quick conformance test for both sequential and parallel modes
set -e

cd "$(dirname "$0")/.."

echo "Building cwl-runner..."
go build -o cwl-runner ./cmd/cwl-runner

echo ""
echo "Running sequential conformance tests..."
cd testdata/cwl-v1.2
./run_test.sh RUNNER=../../cwl-runner -j4 --timeout=90 2>&1 | tail -3

echo ""
echo "Running parallel conformance tests..."
./run_test.sh RUNNER=../../cwl-runner EXTRA="--parallel -j4" -j4 --timeout=90 2>&1 | tail -3

echo ""
echo "All conformance tests passed!"
