#!/usr/bin/env bash
#
# parse-timing.sh - Analyze CWL conformance test timing from JUnit XML
#
# Usage:
#   ./scripts/parse-timing.sh [options] [file1.xml] [file2.xml] ...
#
# Options:
#   -n, --top N     Show top N slowest tests (default: 20)
#   -a, --all       Show all tests sorted by duration
#   -c, --compare   Compare timing across multiple XML files
#   -f, --failed    Show only failed tests with timing
#   -s, --summary   Show summary statistics only
#   -h, --help      Show this help message
#
# If no files specified, searches for conformance-*-timing.xml in project root.
#
# Examples:
#   ./scripts/parse-timing.sh                                    # Auto-find XMLs, show top 20
#   ./scripts/parse-timing.sh -n 10 conformance-cwl-runner-timing.xml
#   ./scripts/parse-timing.sh --compare                          # Compare all modes
#   ./scripts/parse-timing.sh --failed distributed-none-timing.xml
#

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Defaults
TOP_N=20
SHOW_ALL=false
COMPARE_MODE=false
FAILED_ONLY=false
SUMMARY_ONLY=false
XML_FILES=()

# Colors
if [ -t 1 ]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    CYAN='\033[0;36m'
    BOLD='\033[1m'
    DIM='\033[2m'
    NC='\033[0m'
else
    RED='' GREEN='' YELLOW='' CYAN='' BOLD='' DIM='' NC=''
fi

usage() {
    sed -n '2,20p' "$0" | sed 's/^# \?//'
    exit 0
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -n|--top) TOP_N="$2"; shift 2 ;;
        -a|--all) SHOW_ALL=true; shift ;;
        -c|--compare) COMPARE_MODE=true; shift ;;
        -f|--failed) FAILED_ONLY=true; shift ;;
        -s|--summary) SUMMARY_ONLY=true; shift ;;
        -h|--help) usage ;;
        -*) echo "Unknown option: $1"; usage ;;
        *) XML_FILES+=("$1"); shift ;;
    esac
done

# Auto-find XML files if none specified
if [ ${#XML_FILES[@]} -eq 0 ]; then
    for f in "$PROJECT_DIR"/conformance-*-timing.xml; do
        [ -f "$f" ] && XML_FILES+=("$f")
    done
fi

if [ ${#XML_FILES[@]} -eq 0 ]; then
    echo "No timing XML files found. Run conformance tests first:"
    echo "  ./scripts/run-all-tests.sh -m cwl-runner"
    exit 1
fi

# Python script for XML parsing and analysis
analyze_xml() {
    python3 - "$@" << 'PYTHON_SCRIPT'
import sys
import xml.etree.ElementTree as ET
import os
import json

def parse_junit_xml(filepath):
    """Parse JUnit XML and return list of test results with timing."""
    tree = ET.parse(filepath)
    root = tree.getroot()
    tests = []
    for tc in root.findall('.//testcase'):
        name = tc.get('name', '')
        time_s = float(tc.get('time', '0'))
        short_name = tc.get('file', '')
        tags = tc.get('class', '')

        # Check for failure
        failure = tc.find('failure')
        failed = failure is not None
        failure_msg = failure.get('message', '') if failure is not None else ''

        tests.append({
            'name': name,
            'short_name': short_name,
            'time': time_s,
            'tags': tags,
            'failed': failed,
            'failure_msg': failure_msg,
        })
    return tests

def format_time(seconds):
    """Format seconds as human-readable duration."""
    if seconds < 1:
        return f"{seconds*1000:.0f}ms"
    if seconds < 60:
        return f"{seconds:.1f}s"
    m = int(seconds) // 60
    s = seconds - m * 60
    return f"{m}m {s:.0f}s"

def print_summary(mode_name, tests):
    """Print summary statistics for a set of tests."""
    times = [t['time'] for t in tests]
    passed = [t for t in tests if not t['failed']]
    failed = [t for t in tests if t['failed']]

    total = sum(times)
    avg = total / len(times) if times else 0
    median_idx = len(times) // 2
    sorted_times = sorted(times)
    median = sorted_times[median_idx] if times else 0
    p90 = sorted_times[int(len(sorted_times) * 0.9)] if times else 0
    p95 = sorted_times[int(len(sorted_times) * 0.95)] if times else 0
    p99 = sorted_times[int(len(sorted_times) * 0.99)] if times else 0
    max_time = max(times) if times else 0
    min_time = min(times) if times else 0

    print(f"\n  {'Total tests:':<20} {len(tests)}")
    print(f"  {'Passed:':<20} {len(passed)}")
    print(f"  {'Failed:':<20} {len(failed)}")
    print(f"  {'Total time:':<20} {format_time(total)}")
    print(f"  {'Average:':<20} {format_time(avg)}")
    print(f"  {'Median:':<20} {format_time(median)}")
    print(f"  {'P90:':<20} {format_time(p90)}")
    print(f"  {'P95:':<20} {format_time(p95)}")
    print(f"  {'P99:':<20} {format_time(p99)}")
    print(f"  {'Min:':<20} {format_time(min_time)}")
    print(f"  {'Max:':<20} {format_time(max_time)}")

    # Time distribution histogram
    buckets = [0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600]
    counts = [0] * (len(buckets) + 1)
    for t in times:
        placed = False
        for i, b in enumerate(buckets):
            if t <= b:
                counts[i] += 1
                placed = True
                break
        if not placed:
            counts[-1] += 1

    print(f"\n  Time Distribution:")
    labels = [f"<={format_time(b)}" for b in buckets] + [f">{format_time(buckets[-1])}"]
    max_count = max(counts) if counts else 1
    for label, count in zip(labels, counts):
        if count > 0:
            bar_len = int(40 * count / max_count)
            bar = '#' * bar_len
            print(f"  {label:>10}: {count:4d} {bar}")

def print_test_table(tests, top_n=None, show_all=False, failed_only=False):
    """Print test timing table."""
    filtered = tests
    if failed_only:
        filtered = [t for t in tests if t['failed']]

    sorted_tests = sorted(filtered, key=lambda t: t['time'], reverse=True)

    if not show_all and top_n:
        sorted_tests = sorted_tests[:top_n]

    if not sorted_tests:
        print("  No matching tests found.")
        return

    # Column widths
    max_name_len = min(50, max(len(t['short_name']) for t in sorted_tests))

    # Header
    header = f"  {'#':<5} {'Test':<{max_name_len}}  {'Time':>10}  {'Status':>8}"
    print(header)
    print("  " + "-" * (len(header) - 2))

    for i, t in enumerate(sorted_tests, 1):
        name = t['short_name']
        if len(name) > max_name_len:
            name = name[:max_name_len-3] + "..."
        status = "FAIL" if t['failed'] else "pass"
        time_str = format_time(t['time'])
        print(f"  {i:<5} {name:<{max_name_len}}  {time_str:>10}  {status:>8}")

def print_comparison(file_data):
    """Compare timing across multiple XML files."""
    # Build test index
    all_tests = {}
    modes = []
    for filepath, tests in file_data:
        mode = os.path.basename(filepath).replace('conformance-', '').replace('-timing.xml', '')
        modes.append(mode)
        for t in tests:
            key = t['short_name']
            if key not in all_tests:
                all_tests[key] = {'name': t['name']}
            all_tests[key][mode] = t['time']
            all_tests[key][f'{mode}_failed'] = t['failed']

    if len(modes) < 2:
        print("Need at least 2 XML files for comparison.")
        return

    # Print header
    mode_headers = "  ".join(f"{m:>12}" for m in modes)
    print(f"\n  {'Test':<40}  {mode_headers}  {'Delta':>10}")
    print("  " + "-" * (42 + 14 * len(modes) + 12))

    # Sort by first mode time (descending)
    first_mode = modes[0]
    items = [(k, v) for k, v in all_tests.items() if first_mode in v]
    items.sort(key=lambda x: x[1].get(first_mode, 0), reverse=True)

    for key, data in items[:30]:
        name = key[:40] if len(key) <= 40 else key[:37] + "..."
        times = []
        for m in modes:
            if m in data:
                t = data[m]
                failed = data.get(f'{m}_failed', False)
                marker = " X" if failed else ""
                times.append(f"{format_time(t):>10}{marker}")
            else:
                times.append(f"{'---':>12}")

        # Calculate delta between first two modes
        delta = ""
        if modes[0] in data and modes[1] in data:
            d = data[modes[1]] - data[modes[0]]
            if abs(d) > 0.1:
                sign = "+" if d > 0 else ""
                delta = f"{sign}{format_time(abs(d))}"
                if d > 0:
                    delta = f"+{format_time(d)}"
                else:
                    delta = f"-{format_time(-d)}"

        time_cols = "  ".join(times)
        print(f"  {name:<40}  {time_cols}  {delta:>10}")

# Main
args = sys.argv[1:]

# Parse flags
top_n = 20
show_all = False
compare = False
failed_only = False
summary_only = False
files = []

i = 0
while i < len(args):
    if args[i] in ('-n', '--top'):
        top_n = int(args[i+1])
        i += 2
    elif args[i] in ('-a', '--all'):
        show_all = True
        i += 1
    elif args[i] in ('-c', '--compare'):
        compare = True
        i += 1
    elif args[i] in ('-f', '--failed'):
        failed_only = True
        i += 1
    elif args[i] in ('-s', '--summary'):
        summary_only = True
        i += 1
    else:
        files.append(args[i])
        i += 1

if not files:
    print("No XML files provided", file=sys.stderr)
    sys.exit(1)

# Parse all files
file_data = []
for f in files:
    if not os.path.exists(f):
        print(f"File not found: {f}", file=sys.stderr)
        continue
    tests = parse_junit_xml(f)
    file_data.append((f, tests))

if compare and len(file_data) >= 2:
    print("\n=== Timing Comparison ===")
    print_comparison(file_data)
    print()
    for filepath, tests in file_data:
        mode = os.path.basename(filepath).replace('conformance-', '').replace('-timing.xml', '')
        print(f"\n--- {mode} ---")
        print_summary(mode, tests)
else:
    for filepath, tests in file_data:
        mode = os.path.basename(filepath).replace('conformance-', '').replace('-timing.xml', '')
        print(f"\n{'='*60}")
        print(f"  Mode: {mode}")
        print(f"  File: {os.path.basename(filepath)}")
        print(f"{'='*60}")

        print_summary(mode, tests)

        if not summary_only:
            label = "Failed" if failed_only else ("All" if show_all else f"Top {top_n} Slowest")
            print(f"\n  {label} Tests:")
            print_test_table(tests, top_n=top_n, show_all=show_all, failed_only=failed_only)

        print()
PYTHON_SCRIPT
}

# Build common args
ANALYZE_ARGS=(--top "$TOP_N")
[ "$COMPARE_MODE" = true ] && ANALYZE_ARGS+=(--compare)
[ "$SUMMARY_ONLY" = true ] && ANALYZE_ARGS+=(--summary)
[ "$FAILED_ONLY" = true ] && ANALYZE_ARGS+=(--failed)
[ "$SHOW_ALL" = true ] && ANALYZE_ARGS+=(--all)

analyze_xml "${ANALYZE_ARGS[@]}" "${XML_FILES[@]}"
