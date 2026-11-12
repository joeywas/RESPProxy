package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseDebugFlag(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantDebug bool
		wantArgs  []string
		wantErr   bool
	}{
		{name: "no switch", args: nil},
		{name: "long switch", args: []string{"--debug"}, wantDebug: true},
		{name: "short switch", args: []string{"-debug"}, wantDebug: true},
		{name: "reject service console debug", args: []string{"install", "--debug"}, wantErr: true},
		{name: "reject unknown argument", args: []string{"unknown"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			debug, args, err := parseDebugFlag(test.args)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseDebugFlag(%v) error = %v, wantErr %t", test.args, err, test.wantErr)
			}
			if err == nil && (debug != test.wantDebug || strings.Join(args, " ") != strings.Join(test.wantArgs, " ")) {
				t.Fatalf("parseDebugFlag(%v) = (%t, %v), want (%t, %v)", test.args, debug, args, test.wantDebug, test.wantArgs)
			}
		})
	}
}

func TestLoadConfigClientLimits(t *testing.T) {
	for _, key := range []string{
		"REDIS_CLUSTER_NODES", "MAX_CLIENTS", "CLIENT_IDLE_TIMEOUT",
		"MAX_BACKEND_CONNECTIONS_PER_SESSION", "REDIS_USERNAME", "REDIS_PASSWORD",
		"PROXY_USERNAME", "PROXY_PASSWORD", "REDIS_TLS", "REDIS_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("REDIS_CLUSTER_NODES", "127.0.0.1:6379")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxClients != defaultMaxClients || cfg.clientIdleTimeout != defaultClientIdleTimeout ||
		cfg.maxBackendConnectionsPerSession != defaultMaxBackendConnectionsPerSession {
		t.Fatalf("default limits = (%d, %s, %d), want (%d, %s, %d)",
			cfg.maxClients, cfg.clientIdleTimeout, cfg.maxBackendConnectionsPerSession,
			defaultMaxClients, defaultClientIdleTimeout, defaultMaxBackendConnectionsPerSession)
	}

	t.Setenv("MAX_CLIENTS", "42")
	t.Setenv("CLIENT_IDLE_TIMEOUT", "2m")
	t.Setenv("MAX_BACKEND_CONNECTIONS_PER_SESSION", "8")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxClients != 42 || cfg.clientIdleTimeout != 2*time.Minute || cfg.maxBackendConnectionsPerSession != 8 {
		t.Fatalf("configured limits = (%d, %s, %d), want (42, 2m, 8)",
			cfg.maxClients, cfg.clientIdleTimeout, cfg.maxBackendConnectionsPerSession)
	}
}

func TestClientLimitRejectsExcessConnections(t *testing.T) {
	proxy := &proxy{config: config{maxClients: 1}, clients: make(map[net.Conn]struct{})}
	first, firstPeer := net.Pipe()
	defer firstPeer.Close()
	if !proxy.addClient(first) {
		t.Fatal("addClient() rejected the first client")
	}
	second, secondPeer := net.Pipe()
	defer second.Close()
	defer secondPeer.Close()
	if proxy.addClient(second) {
		t.Fatal("addClient() accepted a client above the configured limit")
	}
	proxy.removeClient(first)
	proxy.clientsWG.Done()
	_ = first.Close()
}

func TestClientIdleTimeoutClosesConnection(t *testing.T) {
	proxy := &proxy{
		config:  config{maxClients: 1, clientIdleTimeout: 25 * time.Millisecond},
		clients: make(map[net.Conn]struct{}),
	}
	server, client := net.Pipe()
	defer client.Close()
	if !proxy.addClient(server) {
		t.Fatal("addClient() rejected the client")
	}
	finished := make(chan struct{})
	go func() {
		proxy.serveClient(server)
		close(finished)
	}()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("client connection remained open after the idle timeout")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("client session did not finish after the idle timeout")
	}
}

func TestBackendLimitEvictsLeastRecentlyUsedConnection(t *testing.T) {
	oldConnection, oldPeer := net.Pipe()
	defer oldPeer.Close()
	recentConnection, recentPeer := net.Pipe()
	defer recentPeer.Close()
	session := &clientSession{backends: map[string]*backendConn{
		"old":    {conn: oldConnection, lastUsed: time.Now().Add(-time.Minute)},
		"recent": {conn: recentConnection, lastUsed: time.Now()},
	}}

	session.evictLeastRecentlyUsedBackend()
	if _, exists := session.backends["old"]; exists {
		t.Fatal("least-recently-used backend was not evicted")
	}
	if _, exists := session.backends["recent"]; !exists {
		t.Fatal("more recently used backend was evicted")
	}
	if _, err := oldPeer.Write([]byte("x")); err == nil {
		t.Fatal("evicted backend connection remained open")
	}
}

func TestWriteDebugOperationDoesNotIncludeCommandArguments(t *testing.T) {
	var output bytes.Buffer
	if err := writeDebugOperation(&output, "HSET", time.Now()); err != nil {
		t.Fatal(err)
	}
	line := output.String()
	if !strings.Contains(line, "command=HSET result=success duration=") {
		t.Fatalf("debug output does not include command outcome and duration: %q", line)
	}
	for _, sensitive := range []string{"session-key", "session-value", "password"} {
		if strings.Contains(line, sensitive) {
			t.Fatalf("debug output contains sensitive data %q: %q", sensitive, line)
		}
	}
}

func TestIsClientDisconnect(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "connection reset", err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, want: true},
		{name: "Windows socket read reset", err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("wsarecv: connection reset")}, want: true},
		{name: "closed connection", err: net.ErrClosed, want: true},
		{name: "broken pipe", err: syscall.EPIPE, want: true},
		{name: "write failure", err: &net.OpError{Op: "write", Net: "tcp", Err: errors.New("write failed")}, want: false},
		{name: "malformed RESP", err: errors.New("invalid RESP line terminator"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isClientDisconnect(test.err); got != test.want {
				t.Fatalf("isClientDisconnect(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestListenerStopsAndClosesClients(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}

	p := &proxy{
		config:  config{listenAddress: address, maxClients: 1, clientIdleTimeout: time.Minute},
		clients: make(map[net.Conn]struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan error, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- p.listenAndServe(ctx, ready)
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	client, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer client.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.clientsMu.Lock()
		accepted := len(p.clients) > 0
		p.clientsMu.Unlock()
		if accepted {
			break
		}
		time.Sleep(time.Millisecond)
	}
	p.clientsMu.Lock()
	accepted := len(p.clients) > 0
	p.clientsMu.Unlock()
	if !accepted {
		cancel()
		t.Fatal("listener did not accept client connection")
	}

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("listenAndServe() = %v, want nil on shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listenAndServe() did not stop promptly")
	}
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("client connection remained open after listener shutdown")
	}
}
