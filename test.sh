#!/bin/bash

set -e

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[kinetix]${NC} $1"; }
warn() { echo -e "${YELLOW}[kinetix]${NC} $1"; }
err() { echo -e "${RED}[kinetix]${NC} $1"; }

cleanup() {
    warn "shutting down..."
    kill $ORCHESTRATOR_PID 2>/dev/null
    kill $WORKER1_PID 2>/dev/null
    kill $WORKER2_PID 2>/dev/null
    kill $WORKER3_PID 2>/dev/null
    exit 0
}
trap cleanup SIGINT SIGTERM

log "starting orchestrator..."
cd orchestrator
go run . --port-grpc 50051 --port-http 5000 &
ORCHESTRATOR_PID=$!
cd ../

log "waiting for orchestrator to be ready..."
sleep 3

log "starting worker 1..."
cd agent
cargo run &
WORKER1_PID=$!
cd ../

sleep 1

log "starting worker 2..."
cd agent
cargo run &
WORKER2_PID=$!
cd ../

sleep 1

log "starting worker 3..."
cd agent
cargo run &
WORKER3_PID=$!
cd ../

log "waiting for workers to connect..."
sleep 3

WASM="(module (func (export \"run\") (result i32) i32.const 10 i32.const 32 i32.add))"

log "starting job loop — ctrl+c to stop"
echo ""

JOB_COUNT=0
while true; do
    JOB_COUNT=$((JOB_COUNT + 1))
    
    RESPONSE=$(curl --location 'http://localhost:5000/api/job' \
    --header 'Content-Type: application/json' \
    --data '{
    "type": "COMPUTE",
    "priority": 1,
    "payload": {
        "string_wasm": "(module (func (export \"run\") (result i32) i32.const 42))"
    },
    "fuel_limit": 100
    }')
    
    JOB_ID=$(echo $RESPONSE | grep -o '"job_id":"[^"]*"' | cut -d'"' -f4)
    
    if [ -n "$JOB_ID" ]; then
        log "job #$JOB_COUNT submitted | id: $JOB_ID"
    else
        err "job #$JOB_COUNT failed | response: $RESPONSE"
    fi
    
    sleep 1
done