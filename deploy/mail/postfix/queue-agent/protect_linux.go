//go:build linux

package main

import "syscall"

const prSetDumpable = 4

// protectProcess impide el volcado de memoria del agente y que otro proceso del contenedor lea su
// memoria o su entorno (que lleva QUEUE_AGENT_API_KEY): sin volcados, con el proceso no volcable.
func protectProcess() error {
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{})
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
