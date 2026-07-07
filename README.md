# Catalogue Service

This repository contains the enhanced **catalogue** service used in the EviRCA benchmark:

> EviRCA: An Evidence-Aware Skill-Based LLM Agent and a Telemetry-Rich Multi-Modal Benchmark for Microservice Root Cause Analysis

The service is derived from the Sock Shop demo application and provides product catalogue data for the benchmark e-commerce workload. In the EviRCA artifact, Sock Shop is modernized and instrumented to support reproducible microservice root cause analysis (RCA) with synchronized metrics, logs, traces, service topology, fault-injection artifacts, and fine-grained labels.

## Role in the Benchmark

The catalogue service is one of the Go services in the enhanced Sock Shop system. It serves product metadata and image assets from a MySQL-backed catalogue database, and participates in the benchmark's request paths through the front-end service.

This fork updates the original Sock Shop catalogue implementation for the RCA benchmark by adding or improving:

- Go 1.22 and Go Modules based builds.
- Prometheus HTTP metrics at `/metrics`, including request latency, request size, response size, in-flight requests, method, route, status code, and websocket labels.
- Zipkin/OpenTracing-compatible distributed tracing with configurable collector endpoints.
- Trace context extraction and trace-tag enrichment for benchmark runs.
- Structured log fields for service, operation, dependency, target, error type, latency, and trace context.
- Panic recovery that records errors in logs and tracing spans.
- Health and product API endpoints compatible with the original Sock Shop clients.

## API

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/catalogue` | List catalogue items. Supports `page`, `size`, `sort`, and comma-separated `tags` query parameters. |
| `GET` | `/catalogue/size` | Count catalogue items, optionally filtered by `tags`. |
| `GET` | `/catalogue/{id}` | Get one catalogue item by ID. |
| `GET` | `/catalogue/images/{file}` | Serve product images. |
| `GET` | `/tags` | List product tags. |
| `GET` | `/health` | Health check endpoint. |
| `GET` | `/metrics` | Prometheus metrics endpoint. |

The OpenAPI-style API specification is available in [`api-spec/catalogue.json`](api-spec/catalogue.json).

## Configuration

The service can be configured with command-line flags:

| Flag | Default | Description |
| --- | --- | --- |
| `-port` | `80` | HTTP listen port. |
| `-images` | `./images/` | Product image directory. |
| `-DSN` | `catalogue_user:default_password@tcp(catalogue-db:3306)/socksdb` | MySQL data source name. |
| `-zipkin` | derived from environment | Zipkin-compatible collector endpoint. |

Tracing collector resolution uses the following environment variables, in order:

- `ZIPKIN`: full collector URL.
- `ZIPKIN_BASE_URL`: base collector URL; `/api/v2/spans` is appended when needed.
- `ZIPKIN_HOST` and `ZIPKIN_PORT`: host and port used to build a collector URL.

If none are provided, the service defaults to the benchmark observability collector at `http://jaeger-collector.observability.svc.cluster.local:9411/api/v2/spans`.

## Build

Build the service locally:

```sh
go build -o cataloguesvc ./cmd/cataloguesvc
```

Build the Docker image:

```sh
docker-compose build
```

## Run Locally

Start the service and its catalogue database with Docker Compose:

```sh
docker-compose up
```

The service is exposed on port `8080` by the compose file:

```sh
curl http://localhost:8080/health
curl http://localhost:8080/catalogue
curl http://localhost:8080/metrics
```

To run the compiled binary directly, provide a reachable MySQL DSN:

```sh
./cataloguesvc \
  -port=8080 \
  -images=./images \
  -DSN='catalogue_user:default_password@tcp(localhost:3306)/socksdb'
```

## Zipkin/Jaeger Test Setup

For a local tracing stack:

```sh
docker-compose -f docker-compose-zipkin.yml build
docker-compose -f docker-compose-zipkin.yml up
```

After the service has generated traffic, inspect traces at:

```text
http://localhost:9411/
```

Stop the stack with:

```sh
docker-compose -f docker-compose-zipkin.yml down
```

## Tests

Run the Go test suite:

```sh
go test ./...
```

The repository also keeps the original Makefile test entry point:

```sh
make test
```

## Research Context

In the EviRCA paper, the enhanced Sock Shop benchmark is used to evaluate multi-granularity microservice RCA. The full benchmark combines upgraded services, Prometheus metrics, logs, distributed traces, service topology, Chaos Mesh fault injection artifacts, workload metadata, and labels for service-level, pod-level, service-fault, and pod-fault diagnosis.

This repository only contains the catalogue service component. It is intended to be used together with the other enhanced Sock Shop services and the benchmark orchestration/telemetry collection pipeline.

## Acknowledgements

This service is based on the original Sock Shop catalogue service from the `microservices-demo` project. The implementation has been adapted for the EviRCA benchmark and artifact.
