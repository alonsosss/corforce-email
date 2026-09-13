// Package smtp reinyecta mensajes de cuarentena por el puerto interno de Postfix
// (590: sin milter, solo desde mynetworks). Sin autenticacion ni TLS a proposito: es la
// misma red interna de la celda y Postfix solo admite a sus redes.
package smtp

import (
	"context"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type Reinjector struct {
	addr string
	helo string
}

// New recibe host:puerto (por defecto postfix:590) y el nombre HELO.
func New(addr, helo string) *Reinjector {
	return &Reinjector{addr: addr, helo: helo}
}

func (r *Reinjector) Reinject(ctx context.Context, sender, rcpt string, msg []byte) error {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		return fmt.Errorf("conectar con %s: %w", r.addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	}
	host, _, _ := net.SplitHostPort(r.addr)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("saludo smtp: %w", err)
	}
	defer client.Close()
	if err := client.Hello(r.helo); err != nil {
		return fmt.Errorf("helo: %w", err)
	}
	// Un remitente vacio o ilegible se reinyecta con sobre vacio (rebote), que es lo
	// que Postfix acepta para un mensaje sin origen conocido.
	if sender = strings.TrimSpace(sender); sender != "" && !strings.Contains(sender, "@") {
		sender = ""
	}
	if err := client.Mail(sender); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := client.Rcpt(rcpt); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		w.Close()
		return fmt.Errorf("escribir mensaje: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("cerrar data: %w", err)
	}
	return client.Quit()
}
