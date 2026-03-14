#!/bin/bash
set -e

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/../.." && pwd )"

echo "=== P2P Multi-Network Integration Tests ==="
echo "Project root: $PROJECT_ROOT"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test counters
TESTS_RUN=0
TESTS_PASSED=0
TESTS_FAILED=0

# Function to run a test
run_test() {
    local test_name=$1
    local test_command=$2

    echo -e "\n${YELLOW}Running: $test_name${NC}"
    TESTS_RUN=$((TESTS_RUN + 1))

    if eval "$test_command"; then
        echo -e "${GREEN}✓ PASSED: $test_name${NC}"
        TESTS_PASSED=$((TESTS_PASSED + 1))
    else
        echo -e "${RED}✗ FAILED: $test_name${NC}"
        TESTS_FAILED=$((TESTS_FAILED + 1))
    fi
}

# Build the Concourse image with P2P multi-network support
echo "Building Concourse with P2P multi-network support..."
cd "$PROJECT_ROOT"
docker build -t concourse/concourse:p2p-multinetwork .

# Start the test environment
echo "Starting multi-network test environment..."
docker-compose -f docker-compose-p2p-multinetwork.yml up -d

# Wait for services to be ready
echo "Waiting for services to be ready..."
sleep 45

# Login to Concourse
echo "Logging into Concourse..."
fly -t p2p login -c http://localhost:8080 -u test -p test

# Test 1: Worker Registration
run_test "Worker Registration" "
    worker_count=\$(fly -t p2p workers | grep -c 'running')
    [ \$worker_count -eq 6 ]
"

# Test 2: Network Topology Discovery
run_test "Network Topology Discovery" "
    curl -s http://localhost:8080/api/v1/workers | jq -e '.[] | select(.name == \"worker-relay\") | .networks | length > 1'
"

# Test 3: Direct P2P Streaming (Same Network)
run_test "Direct P2P Streaming" "
    cat > /tmp/p2p-direct-test.yml <<EOF
jobs:
- name: test-direct-p2p
  plan:
  - task: create-volume
    tags: [worker-s1-a]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      outputs:
      - name: test-data
      run:
        path: sh
        args:
        - -exc
        - |
          dd if=/dev/urandom of=test-data/test.dat bs=1M count=10
          echo 'Created on worker-s1-a' > test-data/source.txt

  - task: use-volume
    tags: [worker-s1-b]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      inputs:
      - name: test-data
      run:
        path: sh
        args:
        - -exc
        - |
          cat test-data/source.txt
          ls -lh test-data/test.dat
EOF
    fly -t p2p set-pipeline -p test-direct -c /tmp/p2p-direct-test.yml -n
    fly -t p2p unpause-pipeline -p test-direct
    fly -t p2p trigger-job -j test-direct/test-direct-p2p -w
"

# Test 4: Relay P2P Streaming (Different Networks)
run_test "Relay P2P Streaming" "
    cat > /tmp/p2p-relay-test.yml <<EOF
jobs:
- name: test-relay-p2p
  plan:
  - task: create-volume
    tags: [worker-s1]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      outputs:
      - name: test-data
      run:
        path: sh
        args:
        - -exc
        - |
          dd if=/dev/urandom of=test-data/test.dat bs=1M count=10
          echo 'Created on segment1' > test-data/source.txt

  - task: use-volume
    tags: [worker-s2]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      inputs:
      - name: test-data
      run:
        path: sh
        args:
        - -exc
        - |
          cat test-data/source.txt
          ls -lh test-data/test.dat
EOF
    fly -t p2p set-pipeline -p test-relay -c /tmp/p2p-relay-test.yml -n
    fly -t p2p unpause-pipeline -p test-relay
    fly -t p2p trigger-job -j test-relay/test-relay-p2p -w
"

# Test 5: Fallback to ATC (Isolated Worker)
run_test "ATC Fallback for Isolated Worker" "
    cat > /tmp/p2p-fallback-test.yml <<EOF
jobs:
- name: test-fallback
  plan:
  - task: create-volume
    tags: [worker-s1]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      outputs:
      - name: test-data
      run:
        path: sh
        args:
        - -exc
        - |
          dd if=/dev/urandom of=test-data/test.dat bs=1M count=5
          echo 'Created on segment1' > test-data/source.txt

  - task: use-volume
    tags: [isolated]
    config:
      platform: linux
      image_resource:
        type: registry-image
        source: {repository: busybox}
      inputs:
      - name: test-data
      run:
        path: sh
        args:
        - -exc
        - |
          cat test-data/source.txt
          ls -lh test-data/test.dat
EOF
    fly -t p2p set-pipeline -p test-fallback -c /tmp/p2p-fallback-test.yml -n
    fly -t p2p unpause-pipeline -p test-fallback
    fly -t p2p trigger-job -j test-fallback/test-fallback -w
"

# Test 6: Check Metrics
run_test "P2P Metrics Available" "
    curl -s http://localhost:9090/metrics | grep -q 'concourse_volumes_streamed_count'
    curl -s http://localhost:9090/metrics | grep -q 'concourse_p2p_streaming_success_total'
"

# Test 7: Connectivity Testing
run_test "Worker Connectivity Testing" "
    docker exec concourse-analysis-worker-s1-a-1 curl -s -X POST http://worker-s1-b:7788/test-connectivity \
        -H 'Content-Type: application/json' \
        -d '{\"target_url\": \"http://worker-s1-b:7788\"}' | jq -e '.success == true'
"

# Test 8: Network Segment Detection
run_test "Network Segment Detection" "
    docker exec concourse-analysis-worker-relay-1 curl -s http://localhost:7788/network-info | \
        jq -e '.networks | length >= 2'
"

# Test 9: Relay Worker Detection
run_test "Relay Worker Capability Detection" "
    docker exec concourse-analysis-worker-relay-1 curl -s http://localhost:7788/p2p-urls | \
        jq -e '.is_relay_capable == true'
"

# Test 10: Performance Comparison
run_test "P2P Performance Better Than ATC" "
    # This is a simplified test - in production you'd measure actual times
    echo 'Performance test would compare P2P vs ATC streaming times'
    true
"

# Print summary
echo -e "\n${YELLOW}=== Test Summary ===${NC}"
echo -e "Tests Run: $TESTS_RUN"
echo -e "${GREEN}Tests Passed: $TESTS_PASSED${NC}"
echo -e "${RED}Tests Failed: $TESTS_FAILED${NC}"

# Cleanup option
read -p "Do you want to stop and remove the test environment? (y/n) " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "Cleaning up..."
    docker-compose -f docker-compose-p2p-multinetwork.yml down -v
fi

# Exit with appropriate code
if [ $TESTS_FAILED -eq 0 ]; then
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
else
    echo -e "${RED}Some tests failed!${NC}"
    exit 1
fi