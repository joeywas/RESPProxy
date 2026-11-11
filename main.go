package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
)

func main() {
	debug, args, err := parseDebugFlag(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	handled, err := runService(args)
	if err != nil {
		log.Fatal(err)
	}
	if handled {
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := runProxy(ctx, nil, debug); err != nil {
		log.Fatal(err)
	}
}

func parseDebugFlag(args []string) (bool, []string, error) {
	debug := false
	remaining := make([]string, 0, len(args))
	for _, arg := range args {
		switch strings.ToLower(arg) {
		case "--debug", "-debug":
			if debug {
				return false, nil, errors.New("debug switch was specified more than once")
			}
			debug = true
		default:
			remaining = append(remaining, arg)
		}
	}
	if len(remaining) > 0 && !isServiceCommand(remaining[0]) {
		return false, nil, fmt.Errorf("unknown argument %q; usage: RESPProxy.exe [--debug]", remaining[0])
	}
	if debug && len(remaining) > 0 {
		return false, nil, errors.New("--debug writes to an interactive console and cannot be used with Windows service commands")
	}
	return debug, remaining, nil
}

func isServiceCommand(arg string) bool {
	switch strings.ToLower(arg) {
	case "install", "uninstall", "start", "stop", "status":
		return true
	default:
		return false
	}
}

func runProxy(ctx context.Context, ready chan<- error, debug bool) error {
	cfg, err := loadConfig()
	if err != nil {
		reportReady(ready, err)
		return err
	}
	cfg.debug = debug
	p := &proxy{
		config:  cfg,
		clients: make(map[net.Conn]struct{}),
	}
	if err := p.refreshTopology(); err != nil {
		reportReady(ready, err)
		return err
	}
	return p.listenAndServe(ctx, ready)
}

func reportReady(ready chan<- error, err error) {
	if ready != nil {
		ready <- err
	}
}
