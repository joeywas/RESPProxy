package main

import (
	"errors"
	"fmt"
	"strings"
)

var singleKeyCommands = stringSet(
	"APPEND", "BITCOUNT", "DECR", "DECRBY", "DUMP", "EXPIRE", "EXPIREAT",
	"EXPIRETIME", "GET", "GETDEL", "GETEX", "GETRANGE", "GETSET", "HDEL",
	"HEXISTS", "HGET", "HGETALL", "HINCRBY", "HINCRBYFLOAT", "HKEYS", "HLEN",
	"HMGET", "HMSET", "HSCAN", "HSET", "HSETNX", "HSTRLEN", "HVALS", "INCR",
	"INCRBY", "INCRBYFLOAT", "LINDEX", "LINSERT", "LLEN", "LPOP", "LPOS",
	"LPUSH", "LPUSHX", "LRANGE", "LREM", "LSET", "LTRIM", "PERSIST", "PEXPIRE",
	"PEXPIREAT", "PEXPIRETIME", "PFADD", "PTTL", "PSETEX", "RESTORE", "RPOP",
	"RPUSH", "RPUSHX", "SADD", "SCARD",
	"SISMEMBER", "SMEMBERS", "SMISMEMBER", "SPOP", "SRANDMEMBER", "SREM",
	"SSCAN", "STRLEN", "SUBSTR", "SET", "SETEX", "SETNX", "SETRANGE", "TTL",
	"TYPE", "ZADD", "ZCARD",
	"ZCOUNT", "ZINCRBY", "ZLEXCOUNT", "ZPOPMAX", "ZPOPMIN", "ZRANGE",
	"ZRANGEBYSCORE", "ZRANGEBYLEX", "ZRANK", "ZREM", "ZREMRANGEBYLEX",
	"ZREMRANGEBYRANK", "ZREMRANGEBYSCORE", "ZREVRANGE", "ZREVRANGEBYSCORE",
	"ZREVRANK", "ZSCAN", "ZSCORE", "ZMSCORE", "GETBIT", "SETBIT", "BITFIELD",
)

var multiKeyCommands = stringSet(
	"DEL", "EXISTS", "MGET", "TOUCH", "UNLINK", "SDIFF", "SINTER", "SUNION",
	"PFCOUNT",
)
var multiPairCommands = stringSet("MSET", "MSETNX")
var twoKeyCommands = stringSet(
	"RENAME", "RENAMENX", "SMOVE", "RPOPLPUSH", "LMOVE",
)
var allowedNoKeyCommands = stringSet("PING", "ECHO", "INFO", "TIME", "COMMAND")

func stringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func commandKeyIndexes(args []string) ([]int, error) {
	if len(args) == 0 {
		return nil, errors.New("empty command")
	}
	command := strings.ToUpper(args[0])
	if _, ok := singleKeyCommands[command]; ok {
		if len(args) < 2 {
			return nil, fmt.Errorf("%s requires a key", command)
		}
		return []int{1}, nil
	}
	if _, ok := multiKeyCommands[command]; ok {
		if len(args) < 2 {
			return nil, fmt.Errorf("%s requires at least one key", command)
		}
		keys := make([]int, len(args)-1)
		for i := range keys {
			keys[i] = i + 1
		}
		return keys, nil
	}
	if _, ok := multiPairCommands[command]; ok {
		if len(args) < 3 || len(args)%2 == 0 {
			return nil, fmt.Errorf("%s requires key/value pairs", command)
		}
		keys := make([]int, 0, (len(args)-1)/2)
		for i := 1; i < len(args); i += 2 {
			keys = append(keys, i)
		}
		return keys, nil
	}
	if command == "SDIFFSTORE" || command == "SINTERSTORE" || command == "SUNIONSTORE" || command == "PFMERGE" {
		if len(args) < 3 {
			return nil, fmt.Errorf("%s requires a destination and at least one source key", command)
		}
		keys := make([]int, len(args)-1)
		for i := range keys {
			keys[i] = i + 1
		}
		return keys, nil
	}
	if _, ok := twoKeyCommands[command]; ok {
		if len(args) < 3 {
			return nil, fmt.Errorf("%s requires two keys", command)
		}
		return []int{1, 2}, nil
	}
	if command == "BITOP" {
		if len(args) < 4 {
			return nil, errors.New("BITOP requires a destination and at least one source key")
		}
		keys := make([]int, len(args)-2)
		for i := range keys {
			keys[i] = i + 2
		}
		return keys, nil
	}
	if _, ok := allowedNoKeyCommands[command]; ok {
		return nil, nil
	}
	return nil, fmt.Errorf("unsupported command %s", command)
}

func commandSlot(args []string) (int, bool, error) {
	keyIndexes, err := commandKeyIndexes(args)
	if err != nil {
		return 0, false, err
	}
	if len(keyIndexes) == 0 {
		return 0, false, nil
	}
	slot := redisSlot(args[keyIndexes[0]])
	for _, index := range keyIndexes[1:] {
		if redisSlot(args[index]) != slot {
			return 0, true, errors.New("multi-key command keys must all belong to the same Redis Cluster slot")
		}
	}
	return slot, true, nil
}

func redisSlot(key string) int {
	keyBytes := []byte(key)
	start := -1
	for i, value := range keyBytes {
		if value == '{' {
			start = i + 1
			break
		}
	}
	if start >= 0 {
		for i := start; i < len(keyBytes); i++ {
			if keyBytes[i] == '}' {
				if i > start {
					keyBytes = keyBytes[start:i]
				}
				break
			}
		}
	}

	var crc uint16
	for _, value := range keyBytes {
		crc ^= uint16(value) << 8
		for bit := 0; bit < 8; bit++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return int(crc % 16384)
}
