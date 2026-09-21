//go:build unix && !linux

package main

import "syscall"

func hardenProcess(*syscall.SysProcAttr) {}
