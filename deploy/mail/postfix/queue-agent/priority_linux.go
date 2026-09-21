//go:build linux

package main

import "syscall"

const (
	ioprioWhoProcess = 1
	ioprioClassIdle  = 3 << 13
)

// deprioritize baja al minimo la prioridad de CPU y de disco de un proceso: `postqueue -j` recorre
// todos los ficheros de la cola y, con millones de mensajes, no puede competir con la entrega de
// Postfix. Es un ajuste de mejor esfuerzo.
func deprioritize(pid int) {
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, pid, 19)
	_, _, _ = syscall.Syscall(syscall.SYS_IOPRIO_SET, ioprioWhoProcess, uintptr(pid), ioprioClassIdle)
}
