package main

import (
	"crypto/tls"
	"net"
	"os"
	"sync"
)

const (
	// maxConnections acota las conexiones abiertas a la vez: el unico cliente es mail-security, que usa una
	// o dos, y el agente corre como root en el contenedor de Postfix.
	maxConnections = 32
	// agentMemoryLimit es el limite blando de memoria del runtime: con el, el recolector trabaja mas antes
	// de que el agente crezca lo suficiente para que el nucleo tenga que elegir a quien matar.
	agentMemoryLimit = 256 << 20
	// oomScoreAdjMax hace del agente el primer candidato del nucleo si el contenedor se queda sin memoria.
	oomScoreAdjMax = "1000"

	oomScoreAdjPath = "/proc/self/oom_score_adj"
)

// preferOOMVictim ofrece al agente al matador de procesos por falta de memoria antes que a Postfix: es un
// auxiliar y su caida no detiene el correo (stop-supervisor.sh la ignora y supervisord lo reinicia).
func preferOOMVictim(path string) error {
	return os.WriteFile(path, []byte(oomScoreAdjMax+"\n"), 0o644)
}

// serverTLSConfig exige TLS 1.3: el unico cliente es mail-security, un binario de Go.
func serverTLSConfig(certs *certReloader) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: certs.get}
}

// limitListener deja aceptar como mucho n conexiones a la vez; el resto espera en la cola del sistema.
type limitListener struct {
	net.Listener
	slots chan struct{}
}

func newLimitListener(l net.Listener, n int) net.Listener {
	return &limitListener{Listener: l, slots: make(chan struct{}, n)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.slots <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitConn{Conn: c, release: sync.OnceFunc(func() { <-l.slots })}, nil
}

type limitConn struct {
	net.Conn
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}
