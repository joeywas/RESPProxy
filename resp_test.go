package main

import (
	"bufio"
	"bytes"
	"reflect"
	"testing"
)

func TestReadCommand(t *testing.T) {
	input := "*3\r\n$4\r\nHGET\r\n$9\r\nsession:1\r\n$4\r\ndata\r\n"
	args, err := readCommand(bufio.NewReader(bytes.NewBufferString(input)))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"HGET", "session:1", "data"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("readCommand() = %#v, want %#v", args, want)
	}
}

func TestReadRESPPreservesReply(t *testing.T) {
	input := "*2\r\n$3\r\none\r\n:2\r\n"
	reply, err := readRESP(bufio.NewReader(bytes.NewBufferString(input)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reply.raw, []byte(input)) {
		t.Fatalf("raw RESP = %q, want %q", reply.raw, input)
	}
}

func TestReadRESPEnforcesFrameBudget(t *testing.T) {
	input := "$5\r\nhello\r\n"
	budget := int64(8)
	if _, err := readRESPWithBudget(bufio.NewReader(bytes.NewBufferString(input)), 0, &budget); err == nil {
		t.Fatal("readRESPWithBudget() accepted a frame larger than its budget")
	}
}
