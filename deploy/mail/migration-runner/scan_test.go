package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeClamd atiende el protocolo INSTREAM de clamd. reply decide la respuesta segun el mensaje
// recibido; con reply nil corta la conexion sin responder.
type fakeClamd struct {
	ln       net.Listener
	received chan []byte
}

func newFakeClamd(t *testing.T, reply func(msg []byte) []byte) *fakeClamd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeClamd{ln: ln, received: make(chan []byte, 16)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn, reply)
		}
	}()
	return f
}

func (f *fakeClamd) serve(conn net.Conn, reply func([]byte) []byte) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	cmd := make([]byte, len("zINSTREAM\x00"))
	if _, err := io.ReadFull(conn, cmd); err != nil || string(cmd) != "zINSTREAM\x00" {
		return
	}
	var msg []byte
	for {
		var size [4]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(size[:])
		if n == 0 {
			break
		}
		chunk := make([]byte, n)
		if _, err := io.ReadFull(conn, chunk); err != nil {
			return
		}
		msg = append(msg, chunk...)
	}
	f.received <- msg
	if out := reply(msg); out != nil {
		_, _ = conn.Write(out)
	}
}

func (f *fakeClamd) addr() string { return f.ln.Addr().String() }

func cleanIfNoEicar(msg []byte) []byte {
	if bytes.Contains(msg, []byte("EICAR-STANDARD-ANTIVIRUS-TEST-FILE")) {
		return []byte("stream: Eicar-Test-Signature FOUND\x00")
	}
	return []byte("stream: OK\x00")
}

func filter(t *testing.T, addr string, maxBytes int64, input []byte) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = runScanFilter(bytes.NewReader(input), &out, &errOut, scanConfig{addr: addr, maxBytes: maxBytes, timeout: 5 * time.Second})
	return code, out.String(), errOut.String()
}

func TestScanFilterMensajeLimpioSeDevuelveIntacto(t *testing.T) {
	clamd := newFakeClamd(t, cleanIfNoEicar)
	msg := []byte("From: a@x.test\r\nSubject: hola\r\n\r\n" + strings.Repeat("cuerpo\r\n", 20000))
	code, out, errOut := filter(t, clamd.addr(), 10<<20, msg)
	if code != 0 || out != string(msg) || errOut != "" {
		t.Fatalf("codigo %d, salida intacta %v, stderr %q", code, out == string(msg), errOut)
	}
	if got := <-clamd.received; !bytes.Equal(got, msg) {
		t.Fatalf("clamd recibio %d bytes, se enviaron %d", len(got), len(msg))
	}
}

func TestScanFilterInfectadoNoEscribeNada(t *testing.T) {
	clamd := newFakeClamd(t, cleanIfNoEicar)
	msg := []byte("Subject: x\r\n\r\nX5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*\r\n")
	code, out, errOut := filter(t, clamd.addr(), 10<<20, msg)
	if code != scanExitInfected || out != "" || !strings.Contains(errOut, "CFM_SCAN_INFECTED") {
		t.Fatalf("codigo %d, salida %q, stderr %q", code, out, errOut)
	}
}

func TestScanFilterFallaCerradoSiClamdNoResponde(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"error de clamd":        func([]byte) []byte { return []byte("INSTREAM size limit exceeded. ERROR\x00") },
		"respuesta desconocida": func([]byte) []byte { return []byte("no entiendo\x00") },
		"sin respuesta":         func([]byte) []byte { return nil },
		"vacia":                 func([]byte) []byte { return []byte("\x00") },
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			clamd := newFakeClamd(t, reply)
			code, out, errOut := filter(t, clamd.addr(), 10<<20, []byte("mensaje"))
			if code != scanExitUnavailable || out != "" || !strings.Contains(errOut, "CFM_SCAN_UNAVAILABLE") {
				t.Fatalf("codigo %d, salida %q, stderr %q", code, out, errOut)
			}
		})
	}
}

func TestScanFilterSinClamdOCaidoRechazaTodo(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	for _, a := range []string{"", addr} {
		code, out, _ := filter(t, a, 1<<20, []byte("mensaje"))
		if code != scanExitUnavailable || out != "" {
			t.Fatalf("direccion %q: codigo %d, salida %q", a, code, out)
		}
	}
}

func TestScanFilterMensajeMayorQueElLimiteNoSeAnaliza(t *testing.T) {
	clamd := newFakeClamd(t, cleanIfNoEicar)
	code, out, errOut := filter(t, clamd.addr(), 1<<20, bytes.Repeat([]byte("a"), 1<<20+1))
	if code != scanExitTooBig || out != "" || !strings.Contains(errOut, "CFM_SCAN_TOOBIG") {
		t.Fatalf("codigo %d, salida largo %d, stderr %q", code, len(out), errOut)
	}
	select {
	case <-clamd.received:
		t.Fatal("un mensaje que no se puede analizar entero no debe enviarse a clamd a medias")
	default:
	}
}

func TestScanFilterClamdLentoVenceElPlazo(t *testing.T) {
	clamd := newFakeClamd(t, func([]byte) []byte { time.Sleep(3 * time.Second); return []byte("stream: OK\x00") })
	var out, errOut bytes.Buffer
	start := time.Now()
	code := runScanFilter(strings.NewReader("mensaje"), &out, &errOut, scanConfig{addr: clamd.addr(), maxBytes: 1 << 20, timeout: 300 * time.Millisecond})
	if code != scanExitUnavailable || out.Len() != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("codigo %d, salida %d, tardo %s", code, out.Len(), time.Since(start))
	}
}

func TestScanConfigFromEnv(t *testing.T) {
	env := map[string]string{"MIGRATION_CLAMD_ADDR": " clamd:3310 ", "MIGRATION_SCAN_MAX_BYTES": "2097152", "MIGRATION_SCAN_TIMEOUT": "30s"}
	cfg := scanConfigFromEnv(func(k string) string { return env[k] })
	if cfg.addr != "clamd:3310" || cfg.maxBytes != 2<<20 || cfg.timeout != 30*time.Second {
		t.Fatalf("%+v", cfg)
	}
	bad := scanConfigFromEnv(func(k string) string { return map[string]string{"MIGRATION_SCAN_MAX_BYTES": "x"}[k] })
	if bad.maxBytes != defaultScanMax || bad.addr != "" {
		t.Fatalf("un valor invalido debe dejar el defecto y no habilitar nada: %+v", bad)
	}
}
