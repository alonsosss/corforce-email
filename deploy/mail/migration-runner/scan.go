package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Codigos de salida de scan-filter. imapsync los reproduce en su linea "Failure: --pipemess ...
// exit value", que es lo que lee el analizador para separar un virus de un antivirus caido sin
// fiarse de texto que pueda venir del origen.
const (
	scanExitInfected    = 3
	scanExitUnavailable = 4
	scanExitTooBig      = 5
	scanExitInternal    = 1

	scanChunk      = 64 << 10
	scanMaxReply   = 1 << 10
	scanDialLimit  = 10 * time.Second
	scanMarkerText = "CFM_SCAN_"
)

var (
	errScanInfected    = errors.New("mensaje infectado")
	errScanUnavailable = errors.New("clamd no disponible")
	errScanTooBig      = errors.New("mensaje mayor que el limite de analisis")
)

// runScanFilter lee un mensaje de in y solo si clamd lo da por limpio lo devuelve intacto por out.
// Cualquier otra cosa (infectado, clamd caido o con una respuesta inesperada, mensaje demasiado
// grande para analizarlo entero) sale con error y sin escribir nada: imapsync no copia el mensaje
// y no borra nada del origen. Falla cerrado.
func runScanFilter(in io.Reader, out, errOut io.Writer, cfg scanConfig) int {
	data, err := io.ReadAll(io.LimitReader(in, cfg.maxBytes+1))
	if err != nil {
		fmt.Fprintln(errOut, scanMarkerText+"INTERNAL")
		return scanExitInternal
	}
	if int64(len(data)) > cfg.maxBytes {
		fmt.Fprintln(errOut, scanMarkerText+"TOOBIG")
		return scanExitTooBig
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	switch err := scanInstream(ctx, cfg.addr, data); {
	case err == nil:
	case errors.Is(err, errScanInfected):
		fmt.Fprintln(errOut, scanMarkerText+"INFECTED")
		return scanExitInfected
	default:
		fmt.Fprintln(errOut, scanMarkerText+"UNAVAILABLE")
		return scanExitUnavailable
	}
	if _, err := out.Write(data); err != nil {
		return scanExitInternal
	}
	return 0
}

type scanConfig struct {
	addr     string
	maxBytes int64
	timeout  time.Duration
}

// scanConfigFromEnv toma del entorno lo que el ejecutor pone al lanzar imapsync. Sin direccion el
// filtro no analiza nada, y por eso rechaza todo.
func scanConfigFromEnv(getenv func(string) string) scanConfig {
	cfg := scanConfig{addr: strings.TrimSpace(getenv("MIGRATION_CLAMD_ADDR")), maxBytes: defaultScanMax, timeout: defaultScanLimit}
	env := func(name string) string { return strings.TrimSpace(getenv(name)) }
	if n, err := envInt64(env, "MIGRATION_SCAN_MAX_BYTES", defaultScanMax, 1<<20, 1<<30); err == nil {
		cfg.maxBytes = n
	}
	if d, err := envDuration(env, "MIGRATION_SCAN_TIMEOUT", defaultScanLimit, time.Second, time.Hour); err == nil {
		cfg.timeout = d
	}
	return cfg
}

// scanInstream envia el mensaje a clamd por INSTREAM. Solo "stream: OK" es limpio: cualquier otra
// respuesta, incluido pasarse de StreamMaxLength, es un fallo del analisis.
func scanInstream(ctx context.Context, addr string, data []byte) error {
	if addr == "" {
		return errScanUnavailable
	}
	dialer := net.Dialer{Timeout: scanDialLimit}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("%w: %v", errScanUnavailable, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	w := bufio.NewWriter(conn)
	if _, err := w.WriteString("zINSTREAM\x00"); err != nil {
		return fmt.Errorf("%w: %v", errScanUnavailable, err)
	}
	var size [4]byte
	for off := 0; off < len(data); off += scanChunk {
		end := min(off+scanChunk, len(data))
		binary.BigEndian.PutUint32(size[:], uint32(end-off))
		if _, err := w.Write(size[:]); err != nil {
			return fmt.Errorf("%w: %v", errScanUnavailable, err)
		}
		if _, err := w.Write(data[off:end]); err != nil {
			return fmt.Errorf("%w: %v", errScanUnavailable, err)
		}
	}
	binary.BigEndian.PutUint32(size[:], 0)
	if _, err := w.Write(size[:]); err != nil {
		return fmt.Errorf("%w: %v", errScanUnavailable, err)
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("%w: %v", errScanUnavailable, err)
	}

	reply, err := bufio.NewReader(io.LimitReader(conn, scanMaxReply)).ReadString(0)
	if err != nil && reply == "" {
		return fmt.Errorf("%w: %v", errScanUnavailable, err)
	}
	reply = strings.TrimRight(reply, "\x00\r\n ")
	switch {
	case strings.HasSuffix(reply, " OK"):
		return nil
	case strings.HasSuffix(reply, " FOUND"):
		return errScanInfected
	default:
		return errScanUnavailable
	}
}
