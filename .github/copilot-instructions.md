# Repository Guidelines

## Architecture

RESPProxy is a Go RESP2 TCP proxy for standalone-style Redis clients accessing Redis Cluster. `main.go` reads environment configuration, fetches the cluster slot map, and starts the listener. `proxy.go` manages client sessions, backend connections, authentication, topology, `MOVED`/`ASK` handling, and graceful shutdown. `service_windows.go` implements Windows Service Control Manager integration; `service_other.go` provides the non-Windows command stub. `resp.go` parses RESP2 requests and preserves backend replies. `routing.go` owns the explicit command allowlist, key-position rules, same-slot checks, and Redis hash-slot calculation.

Session values are opaque. Do not log command arguments, keys, payloads, or credentials. Unsupported commands should fail explicitly. Multi-key commands must not be split across slots or nodes.

## Build and Test

Run the full test suite with `go test ./...`; run a focused test with `go test -run '^TestRedisSlot$' .`. Build with `go build ./...`; create the Windows executable with `go build -o RESPProxy.exe .`.

## Protocol and Routing Conventions

The proxy currently supports RESP2 only. Keep command support explicit in `routing.go`, including exact key extraction for each command, and add routing tests for multi-key and hash-tag behavior. Redis Cluster only supports database 0; `SELECT 0` is handled locally. Client-facing `AUTH` and backend Redis credentials are separate configuration settings.
