#!/bin/bash
set -eo pipefail

# 1. Configuration
TEST_PORT=8090
SCRAPE_INTERVAL="1s"
BINARY="./gitlab-procs-exporter"

echo "=== STARTING GITLAB PROCESS EXPORTER E2E TESTS ==="

# 2. Check compiled binary exists
if [ ! -f "$BINARY" ]; then
    echo "ERROR: Compiled binary not found at $BINARY"
    exit 1
fi

# 3. Spin up exporter in background on test port
echo "Launching exporter on port $TEST_PORT..."
$BINARY --port=$TEST_PORT --interval=$SCRAPE_INTERVAL > /dev/null 2>&1 &
EXPORTER_PID=$!

# Ensure process is killed on script exit
cleanup() {
    echo "Cleaning up exporter process (PID: $EXPORTER_PID)..."
    kill -9 $EXPORTER_PID || true
}
trap cleanup EXIT

# 4. Wait for exporter to become responsive
echo "Waiting for exporter to start..."
STARTED=false
for i in {1..15}; do
    if curl -s "http://localhost:$TEST_PORT/" > /dev/null; then
        echo "Exporter is responsive on port $TEST_PORT!"
        STARTED=true
        break
    fi
    sleep 0.5
done

if [ "$STARTED" = false ]; then
    echo "ERROR: Exporter failed to start within timeout."
    exit 1
fi

# 5. Test Endpoint: Embedded Dashboard index page
echo "Testing Dashboard index page '/'..."
DASHBOARD=$(curl -s "http://localhost:$TEST_PORT/")
if ! echo "$DASHBOARD" | grep -q "GitLab Process History Exporter"; then
    echo "FAIL: Embedded Dashboard HTML was not served correctly."
    exit 1
fi
echo "PASS: Dashboard served successfully!"

# 6. Test Endpoint: Prometheus Scrape /metrics
echo "Testing /metrics endpoint..."
METRICS=$(curl -s "http://localhost:$TEST_PORT/metrics")
if ! echo "$METRICS" | grep -q "gitlab_process_cpu_seconds_total"; then
    echo "FAIL: Metric 'gitlab_process_cpu_seconds_total' not found in scrape output."
    exit 1
fi
if ! echo "$METRICS" | grep -q "gitlab_process_info"; then
    echo "FAIL: Metric 'gitlab_process_info' not found in scrape output."
    exit 1
fi
echo "PASS: /metrics returned valid Prometheus statistics!"

# 7. Test Endpoint: REST API Processes list /api/processes
echo "Testing /api/processes endpoint..."
PROCESSES=$(curl -s "http://localhost:$TEST_PORT/api/processes")
if [ -z "$PROCESSES" ] || [ "$PROCESSES" = "[]" ]; then
    echo "FAIL: /api/processes returned empty or invalid JSON."
    exit 1
fi
if ! echo "$PROCESSES" | grep -q "pid"; then
    echo "FAIL: JSON response does not contain process keys."
    exit 1
fi

# Extract a PID for subsequent testing
TEST_PID=$(echo "$PROCESSES" | grep -o '"pid":[0-9]*' | head -n 1 | cut -d: -f2)
if [ -z "$TEST_PID" ]; then
    echo "FAIL: Could not parse an active PID from /api/processes response."
    exit 1
fi
echo "PASS: /api/processes successfully returned active processes list. Extracted test PID: $TEST_PID"

# 8. Test Endpoint: REST API History timeline /api/history
echo "Gathering historical timeline data for PID $TEST_PID..."
# Sleep to ensure we have at least 2 distinct scrape intervals (interval = 1s)
sleep 2.5

echo "Querying /api/history for PID $TEST_PID..."
HISTORY=$(curl -s "http://localhost:$TEST_PORT/api/history?pid=$TEST_PID")
if [ -z "$HISTORY" ] || [ "$HISTORY" = "{}" ]; then
    echo "FAIL: /api/history query returned empty results."
    exit 1
fi

if ! echo "$HISTORY" | grep -q "$TEST_PID"; then
    echo "FAIL: History JSON does not contain timeline entry for PID $TEST_PID."
    exit 1
fi
echo "PASS: /api/history successfully returned 10-minute historical process metrics!"

echo "=== ALL END-TO-END TESTS PASSED SUCCESSFULY ==="
exit 0
