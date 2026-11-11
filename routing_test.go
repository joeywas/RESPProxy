package main

import "testing"

func TestRedisSlot(t *testing.T) {
	if got := redisSlot("foo"); got != 12182 {
		t.Fatalf("redisSlot(foo) = %d, want 12182", got)
	}
	if got, want := redisSlot("user:{session}:one"), redisSlot("other:{session}:two"); got != want {
		t.Fatalf("hash-tagged keys landed in different slots: %d and %d", got, want)
	}
}

func TestCommandSlot(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		hasKey  bool
		wantErr bool
	}{
		{name: "observed hash command", args: []string{"HGET", "session:1", "data"}, hasKey: true},
		{name: "observed ping", args: []string{"PING"}, hasKey: false},
		{name: "same-slot delete", args: []string{"DEL", "one:{x}", "two:{x}"}, hasKey: true},
		{name: "cross-slot delete", args: []string{"DEL", "one", "two"}, hasKey: true, wantErr: true},
		{name: "same-slot multi-set", args: []string{"MSET", "one:{x}", "a", "two:{x}", "b"}, hasKey: true},
		{name: "same-slot bit operation", args: []string{"BITOP", "AND", "dest:{x}", "source:{x}"}, hasKey: true},
		{name: "cross-slot bit operation", args: []string{"BITOP", "AND", "dest", "source"}, hasKey: true, wantErr: true},
		{name: "unknown command", args: []string{"FLUSHALL"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, hasKey, err := commandSlot(test.args)
			if (err != nil) != test.wantErr {
				t.Fatalf("commandSlot(%v) error = %v, wantErr %t", test.args, err, test.wantErr)
			}
			if err == nil && hasKey != test.hasKey {
				t.Fatalf("commandSlot(%v) hasKey = %t, want %t", test.args, hasKey, test.hasKey)
			}
		})
	}
}
