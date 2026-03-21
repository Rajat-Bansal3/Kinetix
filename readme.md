# Kinetix

> A distributed WASM task orchestrator. Send code, watch it run on a cluster of Rust workers. Simple idea, surprisingly deep rabbit hole.

## What is this?

Kinetix is a distributed task execution system where you submit WebAssembly binaries via HTTP, and a cluster of Rust workers executes them — with fuel limits, memory caps, health monitoring, and automatic fault recovery.

Built this to learn distributed systems properly. Ended up learning more than expected.

## Architecture

```
HTTP Client
     │
     ▼
┌─────────────┐     Redis Queue      ┌──────────────────┐
│ Orchestrator│ ------------------►  │  tasks:pending   │
│   (Go)      │ <----------------->  │  tasks:processing│
└─────────────┘                      └──────────────────┘
     │
     │  gRPC Bidirectional Stream
     │
     |------------------|------------------|
     ▼                  ▼                  ▼
┌─────────┐        ┌─────────┐        ┌─────────┐
│ Worker 1│        │ Worker 2│        │ Worker 3│
│ (Rust)  │        │ (Rust)  │        │ (Rust)  │
└─────────┘        └─────────┘        └─────────┘
```

## Features

**Orchestrator (Go)**
- HTTP API for job ingestion
- gRPC bidirectional streaming to workers
- Round-robin task scheduling
- Redis-backed job queue with pending/processing/completed states
- Heartbeat-based health monitoring
- Reaper — kills dead workers, reclaims their jobs
- Retry logic with dead letter queue after 3 failures

**Worker (Rust)**
- Connects to orchestrator via persistent gRPC stream
- Executes arbitrary WASM binaries using `wasmtime`
- Fuel limits — caps CPU instructions per task
- Memory limits — derived from worker's available memory, prevents OOM
- Reports results back through the stream
- Concurrent task execution via `tokio::task::spawn_blocking`
- Graceful shutdown when orchestrator disconnects

## How it works

1. POST a job with a WASM binary (WAT string or base64 encoded)
2. Orchestrator queues it in Redis
3. Dispatcher picks it up, assigns to a worker via gRPC stream
4. Worker executes the WASM in an isolated `wasmtime` store
5. Result reported back — success, failure, or rejection
6. On failure — requeued for retry 
7. On worker death — reaper reclaims all its jobs back to pending

## Stack

| Component    | Tech                                          |
| ------------ | --------------------------------------------- |
| Orchestrator | Go, gRPC, Redis, Echo                         |
| Worker       | Rust, Tokio, Tonic, Prost, Wasmtime           |
| Queue        | Redis (BRPOPLPUSH)                            |
| Protocol     | Protobuf / gRPC bidirectional streaming, http |
| WASM Runtime | Wasmtime with fuel + memory limits            |

## Getting Started

A quick start test.sh script is already present.

prerequisites:
- running redis on port 6379

expected output 
```docx
executing task , TaskAssignment { task_id: "f3b69b33-0379-40d1-b2c0-2335dc59b921", wasm_binary: ...bytes here ..., fuel_limit: 100, env: {} }
2026/03/22 03:17:33 Task f3b69b33-0379-40d1-b2c0-2335dc59b921 completed | status: SUCCESS | exit_code: 42 | error:
```

```bash
# starting queue ( redis )
docker run -d -p 6379:6379 redis
#starting orchestrator
cd orchestrator
go run . --port-grpc 50051 --port-http 5000 --redis-url "localhost:6379"

#starting workers
cd worker
cargo run
```

## Submit a job

```bash
curl --location 'http://localhost:5000/api/job' \
--header 'Content-Type: application/json' \
--data '{
  "type": "COMPUTE",
  "priority": 1,
  "payload": {
    "string_wasm": "(module (func (export \"run\") (result i32) i32.const 42))"
  },
  "fuel_limit": 100
}'
```

Response:
```json
{
  "job_id": "550e8400-e29b-41d4-a716-446655440000",
  "message": "queued"
}
```

## WASM Contract

Every WASM binary submitted must export a `run` function returning `i32`:

```wat
(module
  (func (export "run") (result i32)
    i32.const 0  ;; 0 = success
  )
)
```

Exit codes:
- `0` — success
- anything else — treated as failure, job requeued

Optional host imports available:
```wat
(import "host" "log" (func (param i32 i32)))
```

## Fault Tolerance

| Scenario              | Behaviour                                           |
| --------------------- | --------------------------------------------------- |
| Worker dies mid-task  | Reaper detects via heartbeat timeout, reclaims jobs |
| Task fails            | Requeued to pending, retried                        |
| Task rejected (OOM)   | Requeued immediately                                |
| All retries exhausted | Moved to dead letter queue                          |
| No workers available  | Job stays in pending until a worker connects        |

## What I learned building this

- gRPC bidirectional streaming is not just request/response with extra steps — stream lifecycle, backpressure, and deadlocks from empty channels are real
- Never hold a mutex while doing IO
- `wasmtime` fuel limits are per-instruction, not per-second — set them higher than you think
- `spawn_blocking` vs `spawn` matters a lot when you have CPU-bound work inside an async runtime
- Redis `BRPOPLPUSH` is atomic — use it, don't roll your own
  
## refferences
- https://grpc.io/
- https://gobyexample.com/  - for go syntax 
- https://grpc.io/docs/languages/go/basics/ - for grpc using go had examples in github repo
- https://docs.rs/wasmtime/latest/wasmtime/ - wasm time 
- ai for a little debugging and feature suggestion