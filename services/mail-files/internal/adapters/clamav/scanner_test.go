package clamav

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
)

const eicar = `X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`

// fakeClamd habla INSTREAM como clamd: reensambla los trozos y responde FOUND si llega la firma de
// prueba EICAR, OK si no, o la respuesta fija que se le de.
func fakeClamd(t *testing.T, fixed string) (string, <-chan int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	received := make(chan int, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		cmd := make([]byte, len("zINSTREAM\x00"))
		if _, err := io.ReadFull(r, cmd); err != nil {
			return
		}
		var data bytes.Buffer
		for {
			var size [4]byte
			if _, err := io.ReadFull(r, size[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(size[:])
			if n == 0 {
				break
			}
			if _, err := io.CopyN(&data, r, int64(n)); err != nil {
				return
			}
		}
		received <- data.Len()
		reply := fixed
		if reply == "" {
			reply = "stream: OK"
			if bytes.Contains(data.Bytes(), []byte(eicar)) {
				reply = "stream: Eicar-Signature FOUND"
			}
		}
		_, _ = conn.Write([]byte(reply + "\x00"))
	}()
	return ln.Addr().String(), received
}

func TestEICARAlFinalDeUnFicheroGrandeSeDetecta(t *testing.T) {
	addr, received := fakeClamd(t, "")
	content := strings.Repeat("0123456789", 50000) + eicar
	err := New(addr, 5*time.Second).Scan(t.Context(), strings.NewReader(content))
	if !errors.Is(err, domain.ErrInfected) {
		t.Fatalf("EICAR: %v", err)
	}
	if n := <-received; n != len(content) {
		t.Fatalf("clamd recibio %d de %d bytes", n, len(content))
	}
}

func TestContenidoLimpio(t *testing.T) {
	addr, _ := fakeClamd(t, "")
	if err := New(addr, 5*time.Second).Scan(t.Context(), strings.NewReader("planos")); err != nil {
		t.Fatal(err)
	}
}

func TestSinVeredictoFallaCerrado(t *testing.T) {
	addr, _ := fakeClamd(t, "INSTREAM size limit exceeded. ERROR")
	if err := New(addr, 5*time.Second).Scan(t.Context(), strings.NewReader("x")); !errors.Is(err, domain.ErrScanUnavailable) {
		t.Fatalf("tope de clamd: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	_ = ln.Close()
	if err := New(closed, time.Second).Scan(t.Context(), strings.NewReader("x")); !errors.Is(err, domain.ErrScanUnavailable) {
		t.Fatalf("clamd caido: %v", err)
	}
}
