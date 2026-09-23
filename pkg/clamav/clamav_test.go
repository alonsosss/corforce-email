package clamav

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeClamd atiende una conexion INSTREAM, reensambla los trozos y responde reply.
func fakeClamd(t *testing.T, reply string) (string, <-chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	received := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		cmd := make([]byte, len("zINSTREAM\x00"))
		if _, err := io.ReadFull(r, cmd); err != nil || string(cmd) != "zINSTREAM\x00" {
			received <- nil
			return
		}
		var data bytes.Buffer
		for {
			var size [4]byte
			if _, err := io.ReadFull(r, size[:]); err != nil {
				received <- nil
				return
			}
			n := binary.BigEndian.Uint32(size[:])
			if n == 0 {
				break
			}
			if _, err := io.CopyN(&data, r, int64(n)); err != nil {
				received <- nil
				return
			}
		}
		received <- data.Bytes()
		_, _ = conn.Write([]byte(reply + "\x00"))
	}()
	return ln.Addr().String(), received
}

func TestScanLimpioReensamblaLosTrozos(t *testing.T) {
	addr, received := fakeClamd(t, "stream: OK")
	payload := bytes.Repeat([]byte("0123456789"), 20000) // varios trozos de 64 KiB
	if err := New(addr, 5*time.Second).Scan(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if got := <-received; !bytes.Equal(got, payload) {
		t.Fatalf("clamd recibio %d bytes de %d", len(got), len(payload))
	}
}

func TestScanInfectado(t *testing.T) {
	addr, _ := fakeClamd(t, "stream: Eicar-Test-Signature FOUND")
	err := New(addr, 5*time.Second).Scan(context.Background(), []byte("X5O!P%@AP"))
	if !errors.Is(err, ErrInfected) || !strings.Contains(err.Error(), "Eicar-Test-Signature") {
		t.Fatalf("got %v", err)
	}
}

func TestScanFallaCerrado(t *testing.T) {
	addr, _ := fakeClamd(t, "INSTREAM size limit exceeded. ERROR")
	if err := New(addr, 5*time.Second).Scan(context.Background(), []byte("x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("respuesta de error: %v", err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().String()
	_ = ln.Close()
	if err := New(closed, time.Second).Scan(context.Background(), []byte("x")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("clamd caido: %v", err)
	}
}
