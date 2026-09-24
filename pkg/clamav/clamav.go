// Package clamav analiza contenido con el clamd de la celda (protocolo INSTREAM). Lo usan los
// servicios que guardan o envian ficheros de usuarios (adjuntos del webmail, imagenes de las
// plantillas); cada uno traduce los dos errores a los de su dominio.
package clamav

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

const (
	chunkBytes = 64 << 10
	maxReply   = 1 << 10
)

var (
	// ErrInfected: clamd encontro una firma. El error lleva la firma.
	ErrInfected = errors.New("clamav: contenido infectado")
	// ErrUnavailable: no hubo un veredicto limpio (clamd caido, tiempo agotado, tamano por
	// encima de StreamMaxLength o una respuesta desconocida). Quien llama falla cerrado.
	ErrUnavailable = errors.New("clamav: analisis no disponible")
)

// Scanner habla con un clamd por TCP. Falla cerrado: cualquier respuesta que no sea un OK
// explicito es ErrUnavailable.
type Scanner struct {
	addr    string
	timeout time.Duration
}

func New(addr string, timeout time.Duration) *Scanner {
	return &Scanner{addr: addr, timeout: timeout}
}

// Scan envia data a clamd. nil significa limpio; si no, el error envuelve ErrInfected o
// ErrUnavailable.
func (s *Scanner) Scan(ctx context.Context, data []byte) error {
	return s.ScanReader(ctx, bytes.NewReader(data))
}

// ScanReader envia a clamd el contenido de r en trozos, sin cargarlo entero en memoria (los
// ficheros grandes del webmail). Igual que Scan: nil es limpio y un fallo de lectura de r cuenta
// como ErrUnavailable, porque sin el contenido completo no hay veredicto.
func (s *Scanner) ScanReader(ctx context.Context, r io.Reader) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("%w: conectar con clamd: %v", ErrUnavailable, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	w := bufio.NewWriter(conn)
	// El prefijo z pide respuestas terminadas en NUL.
	if _, err := w.WriteString("zINSTREAM\x00"); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var size [4]byte
	chunk := make([]byte, chunkBytes)
	for {
		n, rerr := io.ReadFull(r, chunk)
		if n > 0 {
			binary.BigEndian.PutUint32(size[:], uint32(n))
			if _, err := w.Write(size[:]); err != nil {
				return fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				return fmt.Errorf("%w: %v", ErrUnavailable, err)
			}
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			break
		}
		if rerr != nil {
			return fmt.Errorf("%w: leer el contenido: %v", ErrUnavailable, rerr)
		}
	}
	binary.BigEndian.PutUint32(size[:], 0)
	if _, err := w.Write(size[:]); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("%w: enviar a clamd: %v", ErrUnavailable, err)
	}
	reply, err := bufio.NewReader(io.LimitReader(conn, maxReply)).ReadString(0)
	if err != nil && reply == "" {
		return fmt.Errorf("%w: leer respuesta de clamd: %v", ErrUnavailable, err)
	}
	return interpret(strings.TrimRight(reply, "\x00\r\n "))
}

// interpret traduce la respuesta de clamd: "stream: OK", "stream: <firma> FOUND" o un
// error ("INSTREAM size limit exceeded. ERROR").
func interpret(reply string) error {
	switch {
	case strings.HasSuffix(reply, " OK"):
		return nil
	case strings.HasSuffix(reply, " FOUND"):
		signature := strings.TrimSuffix(strings.TrimPrefix(reply, "stream: "), " FOUND")
		return fmt.Errorf("%w: %s", ErrInfected, signature)
	default:
		return fmt.Errorf("%w: respuesta de clamd %q", ErrUnavailable, reply)
	}
}
