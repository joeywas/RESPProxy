# RESPProxy

This was created to fill a niche role:

- Run on Windows (Check out EnvoyProxy if on Linux)
- Accept RESP2 connections from Adobe Coldfusion External Session storage feature
- Proxy connections to a sharded Redis cluster

RESPProxy accepts RESP2 client connections and routes a limited, explicit set of Redis commands to the correct Redis Cluster node. It is intended as a compatibility bridge for clients such as ColdFusion's `redissessionstorage` package that connect to one standalone-style Redis endpoint.

Session values are passed through unchanged. The proxy does not implement Redis storage itself, and it does not make cluster-wide operations appear standalone. Commands outside the supported set are rejected.

## Configuration

Configure the proxy with environment variables:

| Variable | Required | Description |
| --- | --- | --- |
| `REDIS_CLUSTER_NODES` | Yes | Comma-separated Redis Cluster seed addresses, each `host:port`. |
| `LISTEN_ADDR` | No | Client listener address; defaults to `127.0.0.1:6379`. |
| `REDIS_USERNAME` | No | Redis Cluster ACL username. Requires `REDIS_PASSWORD`. |
| `REDIS_PASSWORD` | No | Redis Cluster password. |
| `REDIS_TLS` | No | Set to `true` to use TLS for backend connections; defaults to `false`. |
| `REDIS_TIMEOUT` | No | Backend connection and command timeout as a Go duration; defaults to `5s`. |
| `PROXY_USERNAME` | No | Username clients must use when authenticating to the proxy. Requires `PROXY_PASSWORD`. |
| `PROXY_PASSWORD` | No | Password clients must use to authenticate to the proxy. |
| `MAX_CLIENTS` | No | Maximum concurrent client connections; defaults to `1000`. Excess connections are closed. |
| `CLIENT_IDLE_TIMEOUT` | No | Maximum time to wait for a complete client request; defaults to `5m`. |
| `MAX_BACKEND_CONNECTIONS_PER_SESSION` | No | Maximum backend connections retained per client session; defaults to `16`. The least recently used connection is closed when the limit is reached. |

For example, in PowerShell:

```powershell
$env:REDIS_CLUSTER_NODES = "redis-a:6379,redis-b:6379"
$env:LISTEN_ADDR = "127.0.0.1:6379"
go run .
```

To print one console line for each successful command, start RESPProxy interactively with `--debug`:

```powershell
.\RESPProxy.exe --debug
```

Debug output includes the command name and elapsed time only; it does not print keys, arguments, session values, or credentials. This switch is for interactive console runs and is not available with Windows service management commands. Services continue to write their operational logs to Windows Event Viewer.

The proxy loads its slot map using `CLUSTER SLOTS` at startup and follows `MOVED` and `ASK` redirects. Every node address advertised by the cluster must be reachable from the proxy.

## Supported commands

The explicit routed allowlist is in [`routing.go`](./routing.go). It includes these single-key commands:

`APPEND`, `BITCOUNT`, `BITFIELD`, `DECR`, `DECRBY`, `DUMP`, `EXPIRE`, `EXPIREAT`, `EXPIRETIME`, `GET`, `GETBIT`, `GETDEL`, `GETEX`, `GETRANGE`, `GETSET`, `HDEL`, `HEXISTS`, `HGET`, `HGETALL`, `HINCRBY`, `HINCRBYFLOAT`, `HKEYS`, `HLEN`, `HMGET`, `HMSET`, `HSCAN`, `HSET`, `HSETNX`, `HSTRLEN`, `HVALS`, `INCR`, `INCRBY`, `INCRBYFLOAT`, `LINDEX`, `LINSERT`, `LLEN`, `LPOP`, `LPOS`, `LPUSH`, `LPUSHX`, `LRANGE`, `LREM`, `LSET`, `LTRIM`, `PERSIST`, `PEXPIRE`, `PEXPIREAT`, `PEXPIRETIME`, `PFADD`, `PTTL`, `PSETEX`, `RESTORE`, `RPOP`, `RPUSH`, `RPUSHX`, `SADD`, `SCARD`, `SET`, `SETBIT`, `SETEX`, `SETNX`, `SETRANGE`, `SISMEMBER`, `SMEMBERS`, `SMISMEMBER`, `SPOP`, `SRANDMEMBER`, `SREM`, `SSCAN`, `STRLEN`, `SUBSTR`, `TTL`, `TYPE`, `ZADD`, `ZCARD`, `ZCOUNT`, `ZINCRBY`, `ZLEXCOUNT`, `ZPOPMAX`, `ZPOPMIN`, `ZRANGE`, `ZRANGEBYSCORE`, `ZRANGEBYLEX`, `ZRANK`, `ZREM`, `ZREMRANGEBYLEX`, `ZREMRANGEBYRANK`, `ZREMRANGEBYSCORE`, `ZREVRANGE`, `ZREVRANGEBYSCORE`, `ZREVRANK`, `ZSCAN`, `ZSCORE`, `ZMSCORE`.

The allowlist also includes the multi-key commands `DEL`, `EXISTS`, `MGET`, `TOUCH`, `UNLINK`, `SDIFF`, `SINTER`, `SUNION`, and `PFCOUNT`; the key/value pair commands `MSET` and `MSETNX`; the two-key commands `RENAME`, `RENAMENX`, `SMOVE`, `RPOPLPUSH`, and `LMOVE`; and `SDIFFSTORE`, `SINTERSTORE`, `SUNIONSTORE`, `PFMERGE`, and `BITOP` for destination/source routing.

`PING`, `ECHO`, `INFO`, `TIME`, and `COMMAND` are allowed without a key.

`AUTH` is handled against optional proxy credentials; backend authentication is independent. `SELECT 0` is accepted as a no-op and other database numbers are rejected. `QUIT` closes the client connection. `CLIENT SETNAME`, `CLIENT GETNAME`, and `CLIENT SETINFO` for `LIB-NAME` or `LIB-VER` are handled locally; other `CLIENT` subcommands are rejected.

Multi-key, pair, destination/source, and two-key commands are routed only when all routed keys are in the same Redis Cluster hash slot. The proxy rejects cross-slot requests; it does not split a command into multiple operations. Cluster-wide `SCAN`, `KEYS`, blocking commands, transactions, scripts, Pub/Sub, and cluster administrative operations are not supported. Key-specific `HSCAN`, `SSCAN`, and `ZSCAN` are supported.

The proxy speaks RESP2 to clients and backends. RESP3 handshakes such as `HELLO 3` are not supported in this initial version.

## Build and test

```powershell
go build ./...
go test ./...
```

Run a single test, for example:

```powershell
go test -run '^TestRedisSlot$' .
```

### Uninstall

```powershell
Import-Module "D:\Program Files\Servy\Servy.psm1" -Force
Uninstall-ServyService 'RESPProxy'
```
