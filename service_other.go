//go:build !windows

package main

import (
	"errors"
	"fmt"
	"strings"
)

func runService(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch strings.ToLower(args[0]) {
	case "install", "uninstall", "start", "stop", "status":
		return true, errors.New("Windows service commands are only available in Windows builds")
	default:
		return true, fmt.Errorf("unknown command %q", args[0])
	}
}
