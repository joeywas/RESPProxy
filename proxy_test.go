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
		config:  config{listenAddress: address},
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
