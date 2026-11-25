//go:build unix

package main

import "syscall"

func detachedProcessAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
