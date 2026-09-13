// Package clamav analiza adjuntos con el clamd de la celda (protocolo INSTREAM).
package clamav

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const (
	chunkBytes = 64 << 10
	maxReply   = 1 << 10
)

// Scanner implementa ports.VirusScanner. Falla cerrado: cualquier respuesta que no sea un
// OK explicito es domain.ErrScanUnavailable (incluido superar StreamMaxLength de clamd).
type Scanner struct {
	addr    string
	timeout time.Duration
}

func New(addr string, timeout time.Duration) *Scanner {
	return &Scanner{addr: addr, timeout: timeout}
}

func (s *Scanner) Scan(ctx context.Context, name string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("%w: conectar con clamd: %v", domain.ErrScanUnavailable, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	w := bufio.NewWriter(conn)
	// El prefijo z pide respuestas terminadas en NUL.
	if _, err := w.WriteString("zINSTREAM\x00"); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrScanUnavailable, err)
	}
	var size [4]byte
	for off := 0; off < len(data); off += chunkBytes {
		end := min(off+chunkBytes, len(data))
		binary.BigEndian.PutUint32(size[:], uint32(end-off))
		if _, err := w.Write(size[:]); err != nil {
			return fmt.Errorf("%w: %v", domain.ErrScanUnavailable, err)
		}
		if _, err := w.Write(data[off:end]); err != nil {
			return fmt.Errorf("%w: %v", domain.ErrScanUnavailable, err)
		}
	}
	binary.BigEndian.PutUint32(size[:], 0)
	if _, err := w.Write(size[:]); err != nil {
		return fmt.Errorf("%w: %v", domain.ErrScanUnavailable, err)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("%w: enviar a clamd: %v", domain.ErrScanUnavailable, err)
	}

	reply, err := bufio.NewReader(io.LimitReader(conn, maxReply)).ReadString(0)
	if err != nil && reply == "" {
		return fmt.Errorf("%w: leer respuesta de clamd: %v", domain.ErrScanUnavailable, err)
	}
	return interpret(name, strings.TrimRight(reply, "\x00\r\n "))
}

// interpret traduce la respuesta de clamd: "stream: OK", "stream: <firma> FOUND" o un
// error ("INSTREAM size limit exceeded. ERROR").
func interpret(name, reply string) error {
	switch {
	case strings.HasSuffix(reply, " OK"):
		return nil
	case strings.HasSuffix(reply, " FOUND"):
		signature := strings.TrimSuffix(strings.TrimPrefix(reply, "stream: "), " FOUND")
		return fmt.Errorf("%w: %s en %q", domain.ErrAttachmentInfected, signature, name)
	default:
		return fmt.Errorf("%w: respuesta de clamd %q", domain.ErrScanUnavailable, reply)
	}
}
