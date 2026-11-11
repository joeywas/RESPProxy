package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type config struct {
	listenAddress string
	seeds         []string
	redisUsername string
	redisPassword string
	redisTLS      bool
	redisTimeout  time.Duration
	proxyUsername string
	proxyPassword string
	debug         bool
}

type proxy struct {
	config config

	mu        sync.RWMutex
	slots     [16384]string
	nodes     []string
	rr        atomic.Uint64
	clientsMu sync.Mutex
	clients   map[net.Conn]struct{}
	clientsWG sync.WaitGroup
}

type backendConn struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
}

type clientSession struct {
	reader        *bufio.Reader
	backends      map[string]*backendConn
	authenticated bool
	clientName    string
	clientNameSet bool
}

func loadConfig() (config, error) {
	cfg := config{
		listenAddress: envOr("LISTEN_ADDR", "127.0.0.1:6379"),
		redisUsername: os.Getenv("REDIS_USERNAME"),
		redisPassword: os.Getenv("REDIS_PASSWORD"),
		proxyUsername: os.Getenv("PROXY_USERNAME"),
		proxyPassword: os.Getenv("PROXY_PASSWORD"),
		redisTimeout:  5 * time.Second,
	}
	if raw := strings.TrimSpace(os.Getenv("REDIS_CLUSTER_NODES")); raw != "" {
		for _, seed := range strings.Split(raw, ",") {
			seed = strings.TrimSpace(seed)
			if seed != "" {
				cfg.seeds = append(cfg.seeds, seed)
			}
		}
	}
	if len(cfg.seeds) == 0 {
		return config{}, errors.New("REDIS_CLUSTER_NODES must contain at least one host:port seed")
	}
	for _, seed := range cfg.seeds {
		if _, _, err := net.SplitHostPort(seed); err != nil {
			return config{}, fmt.Errorf("invalid Redis seed %q: %w", seed, err)
		}
	}
	if cfg.redisUsername != "" && cfg.redisPassword == "" {
		return config{}, errors.New("REDIS_USERNAME requires REDIS_PASSWORD")
	}
	if cfg.proxyUsername != "" && cfg.proxyPassword == "" {
		return config{}, errors.New("PROXY_USERNAME requires PROXY_PASSWORD")
	}
	if raw := strings.TrimSpace(os.Getenv("REDIS_TLS")); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("invalid REDIS_TLS value: %w", err)
		}
		cfg.redisTLS = enabled
	}
	if raw := strings.TrimSpace(os.Getenv("REDIS_TIMEOUT")); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 {
			return config{}, errors.New("REDIS_TIMEOUT must be a positive Go duration, such as 5s")
		}
		cfg.redisTimeout = timeout
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func (p *proxy) refreshTopology() error {
	var lastErr error
	for _, seed := range p.config.seeds {
		backend, err := p.dialBackend(seed)
		if err != nil {
			lastErr = err
			continue
		}
		reply, err := backend.execute([]string{"CLUSTER", "SLOTS"}, p.config.redisTimeout)
		backend.conn.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if reply.kind == '-' {
			lastErr = fmt.Errorf("CLUSTER SLOTS returned: %s", reply.bytes)
			continue
		}
		slots, nodes, err := parseClusterSlots(reply, seed)
		if err != nil {
			lastErr = err
			continue
		}
		p.mu.Lock()
		p.slots = slots
		p.nodes = nodes
		p.mu.Unlock()
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no Redis cluster seeds were configured")
	}
	return fmt.Errorf("unable to load Redis Cluster topology: %w", lastErr)
}

func parseClusterSlots(reply *respValue, seed string) ([16384]string, []string, error) {
	var slots [16384]string
	if reply.kind != '*' || len(reply.items) == 0 {
		return slots, nil, errors.New("invalid CLUSTER SLOTS response")
	}
	seedHost, _, err := net.SplitHostPort(seed)
	if err != nil {
		return slots, nil, err
	}
	nodeSet := make(map[string]struct{})
	assigned := 0
	for _, slotRange := range reply.items {
		if len(slotRange.items) < 3 {
			return slots, nil, errors.New("invalid slot range in CLUSTER SLOTS response")
		}
		start, err := respInteger(slotRange.items[0])
		if err != nil {
			return slots, nil, err
		}
		end, err := respInteger(slotRange.items[1])
		if err != nil || start < 0 || end < start || end >= int64(len(slots)) {
			return slots, nil, errors.New("invalid slot boundaries in CLUSTER SLOTS response")
		}
		master := slotRange.items[2]
		if len(master.items) < 2 {
			return slots, nil, errors.New("invalid master address in CLUSTER SLOTS response")
		}
		host := string(master.items[0].bytes)
		if master.items[0].null || host == "" {
			host = seedHost
		}
		port, err := respInteger(master.items[1])
		if err != nil || port <= 0 || port > 65535 {
			return slots, nil, errors.New("invalid master port in CLUSTER SLOTS response")
		}
		address := net.JoinHostPort(host, strconv.FormatInt(port, 10))
		nodeSet[address] = struct{}{}
		for slot := start; slot <= end; slot++ {
			slots[slot] = address
			assigned++
		}
	}
	if assigned == 0 {
		return slots, nil, errors.New("Redis Cluster reported no assigned slots")
	}
	nodes := make([]string, 0, len(nodeSet))
	for node := range nodeSet {
		nodes = append(nodes, node)
	}
	return slots, nodes, nil
}

func respInteger(value *respValue) (int64, error) {
	if value.kind == ':' {
		return value.num, nil
	}
	if value.kind == '$' || value.kind == '+' {
		number, err := strconv.ParseInt(string(value.bytes), 10, 64)
		if err == nil {
			return number, nil
		}
	}
	return 0, errors.New("expected an integer in CLUSTER SLOTS response")
}

func (p *proxy) dialBackend(address string) (*backendConn, error) {
	dialer := &net.Dialer{Timeout: p.config.redisTimeout}
	var conn net.Conn
	var err error
	if p.config.redisTLS {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host})
	} else {
		conn, err = dialer.Dial("tcp", address)
	}
	if err != nil {
		return nil, err
	}
	backend := &backendConn{conn: conn, reader: bufio.NewReader(conn), writer: bufio.NewWriter(conn)}
	if p.config.redisPassword != "" {
		auth := []string{"AUTH", p.config.redisPassword}
		if p.config.redisUsername != "" {
			auth = []string{"AUTH", p.config.redisUsername, p.config.redisPassword}
		}
		reply, authErr := backend.execute(auth, p.config.redisTimeout)
		if authErr != nil || reply.kind != '+' {
			conn.Close()
			if authErr != nil {
				return nil, fmt.Errorf("backend authentication failed: %w", authErr)
			}
			return nil, fmt.Errorf("backend authentication rejected: %s", reply.bytes)
		}
	}
	return backend, nil
}

func (b *backendConn) execute(args []string, timeout time.Duration) (*respValue, error) {
	if err := b.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	if err := writeCommand(b.writer, args); err != nil {
		return nil, err
	}
	if err := b.writer.Flush(); err != nil {
		return nil, err
	}
	reply, err := readRESP(b.reader, 0)
	if err != nil {
		return nil, err
	}
	if err := b.conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return reply, nil
}

func (p *proxy) chooseNode(slot int, hasKey bool) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if hasKey {
		address := p.slots[slot]
		if address == "" {
			return "", fmt.Errorf("Redis Cluster has no owner for slot %d", slot)
		}
		return address, nil
	}
	if len(p.nodes) == 0 {
		return "", errors.New("Redis Cluster topology is unavailable")
	}
	return p.nodes[p.rr.Add(1)%uint64(len(p.nodes))], nil
}

func (p *proxy) execute(session *clientSession, args []string, slot int, hasKey bool) ([]byte, error) {
	address, err := p.chooseNode(slot, hasKey)
	if err != nil {
		return nil, err
	}
	asking := false
	for redirects := 0; redirects <= 5; redirects++ {
		backend, err := session.backend(p, address)
		if err != nil {
			return nil, err
		}
		if asking {
			ack, askErr := backend.execute([]string{"ASKING"}, p.config.redisTimeout)
			if askErr != nil {
				session.dropBackend(address)
				return nil, askErr
			}
			if ack.kind == '-' {
				return ack.raw, nil
			}
		}
		reply, err := backend.execute(args, p.config.redisTimeout)
		if err != nil {
			session.dropBackend(address)
			return nil, err
		}
		if reply.kind != '-' {
			return reply.raw, nil
		}
		redirectAddress, redirectErr := parseRedirect(reply.bytes)
		if redirectErr != nil {
			return reply.raw, nil
		}
		if strings.HasPrefix(string(reply.bytes), "MOVED ") {
			_ = p.refreshTopology()
			p.setSlot(slot, redirectAddress)
			address = redirectAddress
			asking = false
		} else {
			address = redirectAddress
			asking = true
		}
	}
	return nil, errors.New("too many Redis Cluster redirections")
}

func (p *proxy) setSlot(slot int, address string) {
	if slot < 0 || slot >= len(p.slots) {
		return
	}
	p.mu.Lock()
	p.slots[slot] = address
	found := false
	for _, node := range p.nodes {
		if node == address {
			found = true
			break
		}
	}
	if !found {
		p.nodes = append(p.nodes, address)
	}
	p.mu.Unlock()
}

func parseRedirect(message []byte) (string, error) {
	fields := strings.Fields(string(message))
	if len(fields) != 3 || (fields[0] != "MOVED" && fields[0] != "ASK") {
		return "", errors.New("not a Redis Cluster redirection")
	}
	if _, _, err := net.SplitHostPort(fields[2]); err != nil {
		return "", fmt.Errorf("invalid redirect address: %w", err)
	}
	return fields[2], nil
}

func (s *clientSession) backend(p *proxy, address string) (*backendConn, error) {
	if backend := s.backends[address]; backend != nil {
		return backend, nil
	}
	backend, err := p.dialBackend(address)
	if err != nil {
		return nil, err
	}
	s.backends[address] = backend
	return backend, nil
}

func (s *clientSession) dropBackend(address string) {
	if backend := s.backends[address]; backend != nil {
		backend.conn.Close()
		delete(s.backends, address)
	}
}

func (p *proxy) serveClient(conn net.Conn) {
	defer conn.Close()
	defer p.removeClient(conn)
	defer p.clientsWG.Done()
	session := &clientSession{
		reader:        bufio.NewReader(conn),
		backends:      make(map[string]*backendConn),
		authenticated: p.config.proxyPassword == "",
	}
	defer func() {
		for _, backend := range session.backends {
			backend.conn.Close()
		}
	}()

	for {
		args, err := readCommand(session.reader)
		if err != nil {
			if !errors.Is(err, io.EOF) && !isClientDisconnect(err) {
				log.Printf("client protocol error: %v", err)
			}
			return
		}
		started := time.Now()
		reply, closeClient, handled := p.localCommand(session, args)
		if handled {
			if err := writeAll(conn, reply); err != nil || closeClient {
				return
			}
			if p.config.debug && isSuccessfulReply(reply) {
				if err := writeDebugOperation(os.Stdout, strings.ToUpper(args[0]), started); err != nil {
					log.Printf("debug console output failed: %v", err)
				}
			}
			continue
		}
		if !session.authenticated {
			if err := writeAll(conn, []byte("-NOAUTH Authentication required.\r\n")); err != nil {
				return
			}
			continue
		}
		slot, hasKey, err := commandSlot(args)
		if err != nil {
			if writeErr := writeAll(conn, errorReply(err.Error())); writeErr != nil {
				return
			}
			continue
		}
		reply, err = p.execute(session, args, slot, hasKey)
		if err != nil {
			reply = errorReply("backend error: " + err.Error())
		}
		if p.config.debug && isSuccessfulReply(reply) {
			if err := writeDebugOperation(os.Stdout, strings.ToUpper(args[0]), started); err != nil {
				log.Printf("debug console output failed: %v", err)
			}
		}
		if err := writeAll(conn, reply); err != nil {
			return
		}
	}
}

func isSuccessfulReply(reply []byte) bool {
	return len(reply) > 0 && reply[0] != '-'
}

func writeDebugOperation(w io.Writer, command string, started time.Time) error {
	_, err := fmt.Fprintf(w, "DEBUG command=%s result=success duration=%s\n", command, time.Since(started).Round(time.Microsecond))
	return err
}

func isClientDisconnect(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "read" {
		return true
	}
	return errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE)
}

func (p *proxy) localCommand(session *clientSession, args []string) ([]byte, bool, bool) {
	command := strings.ToUpper(args[0])
	switch command {
	case "AUTH":
		if len(args) != 2 && len(args) != 3 {
			return errorReply("wrong number of arguments for 'auth' command"), false, true
		}
		if p.config.proxyPassword == "" {
			session.authenticated = true
			return []byte("+OK\r\n"), false, true
		}
		var username, password string
		if len(args) == 2 {
			username, password = "default", args[1]
		} else if len(args) == 3 {
			username, password = args[1], args[2]
		}
		expectedUsername := p.config.proxyUsername
		if expectedUsername == "" {
			expectedUsername = "default"
		}
		if subtle.ConstantTimeCompare([]byte(username), []byte(expectedUsername)) == 1 &&
			subtle.ConstantTimeCompare([]byte(password), []byte(p.config.proxyPassword)) == 1 {
			session.authenticated = true
			return []byte("+OK\r\n"), false, true
		}
		return []byte("-WRONGPASS invalid username-password pair\r\n"), false, true
	case "SELECT":
		if len(args) == 2 && args[1] == "0" {
			return []byte("+OK\r\n"), false, true
		}
		return errorReply("only database 0 is supported"), false, true
	case "QUIT":
		return []byte("+OK\r\n"), true, true
	case "CLIENT":
		if len(args) < 2 {
			return errorReply("wrong number of arguments for 'client' command"), false, true
		}
		switch strings.ToUpper(args[1]) {
		case "SETINFO":
			if len(args) == 4 && (strings.EqualFold(args[2], "LIB-NAME") || strings.EqualFold(args[2], "LIB-VER")) {
				return []byte("+OK\r\n"), false, true
			}
			return errorReply("unsupported CLIENT SETINFO subcommand"), false, true
		case "SETNAME":
			if len(args) == 3 {
				session.clientName = args[2]
				session.clientNameSet = true
				return []byte("+OK\r\n"), false, true
			}
			return errorReply("wrong number of arguments for 'client setname' command"), false, true
		case "GETNAME":
			if len(args) != 2 {
				return errorReply("wrong number of arguments for 'client getname' command"), false, true
			}
			if !session.clientNameSet {
				return []byte("$-1\r\n"), false, true
			}
			return []byte(fmt.Sprintf("$%d\r\n%s\r\n", len(session.clientName), session.clientName)), false, true
		default:
			return errorReply("unsupported CLIENT subcommand"), false, true
		}
	default:
		return nil, false, false
	}
}

func (p *proxy) listenAndServe(ctx context.Context, ready chan<- error) error {
	listener, err := net.Listen("tcp", p.config.listenAddress)
	if err != nil {
		reportReady(ready, err)
		return err
	}
	reportReady(ready, nil)
	done := make(chan struct{})
	defer close(done)
	defer listener.Close()
	defer func() {
		p.closeClients()
		p.clientsWG.Wait()
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-done:
		}
	}()
	log.Printf("RESP2 proxy listening on %s", p.config.listenAddress)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			return err
		}
		p.addClient(conn)
		go p.serveClient(conn)
	}
	return nil
}

func (p *proxy) addClient(conn net.Conn) {
	p.clientsMu.Lock()
	p.clients[conn] = struct{}{}
	p.clientsWG.Add(1)
	p.clientsMu.Unlock()
}

func (p *proxy) removeClient(conn net.Conn) {
	p.clientsMu.Lock()
	delete(p.clients, conn)
	p.clientsMu.Unlock()
}

func (p *proxy) closeClients() {
	p.clientsMu.Lock()
	defer p.clientsMu.Unlock()
	for conn := range p.clients {
		_ = conn.Close()
	}
}
