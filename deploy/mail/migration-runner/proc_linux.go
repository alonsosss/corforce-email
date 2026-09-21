//go:build linux

package main

import "syscall"

// hardenProcess hace que imapsync muera con el ejecutor: si este cae, ningun hijo sigue copiando
// con credenciales que ya no controla nadie.
func hardenProcess(attr *syscall.SysProcAttr) { attr.Pdeathsig = syscall.SIGKILL }
