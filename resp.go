package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const (
	maxLineSize  = 64 << 10
	maxBulkSize  = 64 << 20
	maxFrameSize = 64 << 20
	maxElements  = 4096
	maxRESPDepth = 64
)

type respValue struct {
	kind  byte
	bytes []byte
	num   int64
	items []*respValue
	null  bool
	raw   []byte
}

func readRESP(r *bufio.Reader, depth int) (*respValue, error) {
	budget := int64(maxFrameSize)
	return readRESPWithBudget(r, depth, &budget)
}

func readRESPWithBudget(r *bufio.Reader, depth int, budget *int64) (*respValue, error) {
	if depth > maxRESPDepth {
		return nil, errors.New("RESP nesting limit exceeded")
	}
	if *budget <= 0 {
		return nil, errors.New("RESP frame exceeds supported size")
	}

	kind, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	*budget = *budget - 1
	line, err := readLine(r, budget)
	if err != nil {
		return nil, err
	}

	value := &respValue{kind: kind}
	value.raw = append(value.raw, kind)
	value.raw = append(value.raw, line...)
	switch kind {
	case '+', '-':
		value.bytes = append([]byte(nil), line[:len(line)-2]...)
	case ':':
		value.num, err = strconv.ParseInt(string(line[:len(line)-2]), 10, 64)
	case '$', '*':
		length, parseErr := strconv.ParseInt(string(line[:len(line)-2]), 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid RESP length: %w", parseErr)
		}
		if length == -1 {
			value.null = true
			return value, nil
		}
		if length < 0 || length > maxBulkSize {
			return nil, errors.New("RESP length is outside supported limits")
		}
		if kind == '$' {
			if length+2 > *budget {
				return nil, errors.New("RESP frame exceeds supported size")
			}
			value.bytes = make([]byte, int(length)+2)
			if _, err := io.ReadFull(r, value.bytes); err != nil {
				return nil, err
			}
			*budget = *budget - length - 2
			if !bytes.HasSuffix(value.bytes, []byte("\r\n")) {
				return nil, errors.New("invalid RESP bulk terminator")
			}
			value.raw = append(value.raw, value.bytes...)
			value.bytes = value.bytes[:len(value.bytes)-2]
			return value, nil
		}
		if length > maxElements {
			return nil, errors.New("RESP array is too large")
		}
		value.items = make([]*respValue, 0, int(length))
		for i := int64(0); i < length; i++ {
			item, readErr := readRESPWithBudget(r, depth+1, budget)
			if readErr != nil {
				return nil, readErr
			}
			value.items = append(value.items, item)
			value.raw = append(value.raw, item.raw...)
		}
	default:
		return nil, fmt.Errorf("unsupported RESP type %q", kind)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid RESP integer: %w", err)
	}
	return value, nil
}

func readLine(r *bufio.Reader, budget *int64) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		if len(line)+len(part) > maxLineSize {
			return nil, errors.New("RESP line is too long")
		}
		if int64(len(part)) > *budget {
			return nil, errors.New("RESP frame exceeds supported size")
		}
		line = append(line, part...)
		*budget = *budget - int64(len(part))
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, err
		}
		break
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, errors.New("invalid RESP line terminator")
	}
	return line, nil
}

func readCommand(r *bufio.Reader) ([]string, error) {
	value, err := readRESP(r, 0)
	if err != nil {
		return nil, err
	}
	if value.kind != '*' || value.null || len(value.items) == 0 {
		return nil, errors.New("expected a non-empty RESP command array")
	}
	if len(value.items) > maxElements {
		return nil, errors.New("command has too many arguments")
	}
	args := make([]string, len(value.items))
	for i, item := range value.items {
		if item.null || (item.kind != '$' && item.kind != '+') {
			return nil, errors.New("command arguments must be non-null strings")
		}
		args[i] = string(item.bytes)
	}
	return args, nil
}

func writeCommand(w io.Writer, args []string) error {
	if _, err := fmt.Fprintf(w, "*%d\r\n", len(args)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := fmt.Fprintf(w, "$%d\r\n", len(arg)); err != nil {
			return err
		}
		if _, err := io.WriteString(w, arg); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func errorReply(message string) []byte {
	message = string(bytes.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, []byte(message)))
	return []byte("-ERR " + message + "\r\n")
}
