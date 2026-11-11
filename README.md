# RESPProxy

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

For testing on wtdotappl82.odot.state.or.us

```powershell
$env:REDIS_CLUSTER_NODES = "10.0.0.25:6379,10.0.0.25:6380,10.0.0.25:6384,10.0.0.26:6382,10.0.0.26:6383,10.0.0.26:6381"
$env:REDIS_CLUSTER_NODES = "10.0.0.25:6379"
$env:LISTEN_ADDR = "127.0.0.1:6378"
& D:\RESPProxy\RESPProxy.exe
```

Use the proxy's listener address in ColdFusion. If client authentication is configured, configure ColdFusion with the proxy credentials. Backend credentials are configured separately. Bind the listener only to a network reachable by trusted clients.

The proxy loads its slot map using `CLUSTER SLOTS` at startup and follows `MOVED` and `ASK` redirects. Every node address advertised by the cluster must be reachable from the proxy.

## Supported commands

The initial session-oriented command set includes the observed commands `DEL`, `HSET`, `SADD`, `SET`, `HGET`, `SISMEMBER`, and `PING`, plus commonly used string, hash, set, list, sorted-set, key-expiration, and key-inspection commands. The current explicit allowlist is in [`routing.go`](./routing.go).

`AUTH` is handled against optional proxy credentials; backend authentication is independent. `SELECT 0` is accepted as a no-op and other database numbers are rejected. Basic `CLIENT SETNAME`, `CLIENT GETNAME`, and `CLIENT SETINFO` metadata operations are handled locally.

Multi-key commands are routed only when all keys are in the same Redis Cluster hash slot. The proxy rejects cross-slot requests; it does not split a command into multiple operations. `SCAN`, `KEYS`, blocking commands, transactions, scripts, Pub/Sub, and cluster-wide administrative operations are not supported. Expand the allowlist only with correct key-position routing and tests for the command's cluster semantics.

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

## Windows service

Using Servy powershell module to install RESPProxy as a service.

```powershell
[Environment]::SetEnvironmentVariable("REDIS_CLUSTER_NODES", "10.0.0.25:6379", "Machine")
[Environment]::SetEnvironmentVariable("LISTEN_ADDR", "127.0.0.1:6378", "Machine")
Import-Module "D:\Program Files\Servy\Servy.psm1" -Force
$installParams = @{
    Name = "ODOT.RESPProxy"
    DisplayName = "ODOT.RESPProxy"
    Description = "REDIS Protocol Proxy for Coldfusion"
    Path = "D:\RESPProxy\RESPProxy.exe"
    StartupDir = "D:\RESPProxy\"
    Stdout = "D:\RESPProxy\stdout_respproxy.log"
    Stderr = "D:\RESPProxy\stderr_respproxy.log"
    EnableSizeRotation = $true
    RotationSize = 5
    MaxRotations = 7
    StartupType = "Automatic"
}
Install-ServyService @installParams
```

The installed service starts automatically with Windows and runs as the least-privilege `NT AUTHORITY\LocalService` account. It writes service logs to Windows Event Viewer under the `RESPProxy` source and is configured to restart after failures. Find logs under **Event Viewer > Windows Logs > Application**. Service stop and Windows shutdown controls close the listener and active client connections before the process exits.

The service process does not inherit environment variables set only in an interactive PowerShell session. Set its required configuration in the machine environment before starting it; restart the service after changing configuration. Treat machine-level environment variables containing passwords as sensitive. 

### Uninstall

```powershell
Import-Module "D:\Program Files\Servy\Servy.psm1" -Force
Uninstall-ServyService 'ODOT.RESPProxy'
```
