//go:build linux

package main

import "syscall"

const prSetDumpable = 4

// protectProcess marca al ejecutor como no volcable: sin ello /proc/<pid>/environ, /proc/<pid>/mem y
// el ptrace de cualquier proceso del mismo usuario (imapsync corre como el ejecutor y habla con
// servidores ajenos) darian la clave del servicio y la contrasena del maestro de Dovecot. Tambien
// impide el volcado de memoria. Los hijos que se lanzan con exec recuperan su estado normal.
func protectProcess() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
