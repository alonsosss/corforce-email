package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func normalLine(id, queue string, arrival int64) string {
	return fmt.Sprintf(`{"queue_name": "%s", "queue_id": "%s", "arrival_time": %d, "message_size": 10, "forced_expire": false, "sender": "a@b.c", "recipients": [{"address": "x@y.z", "delay_reason": "connect timed out"}]}`+"\n", queue, id, arrival)
}

// Un mensaje con decenas de miles de destinatarios produce una linea de `postqueue -j` mayor que el
// tope de lectura. Antes hacia fallar el listado ENTERO (bufio.ErrTooLong) y, mientras el mensaje siguiera
// en cola, ni la pantalla ni la vigilancia veian nada: justo lo que hay que poder listar para
// encontrarlo y borrarlo.
func TestUnMensajeGiganteNoImpideListarLaCola(t *testing.T) {
	var b strings.Builder
	b.WriteString(normalLine("AAAAA11111", "deferred", 1700000300))
	b.WriteString(`{"queue_name": "deferred", "queue_id": "GIGANTE0001", "arrival_time": 1700000100, "message_size": 777, "forced_expire": false, "sender": "spam@ejemplo.org", "recipients": [`)
	for i := 0; b.Len() < maxLineBytes+(1<<20); i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, `{"address": "u%d@ejemplo.org", "delay_reason": "connect to ejemplo.org: timeout"}`, i)
	}
	b.WriteString("]}\n")
	b.WriteString(normalLine("BBBBB22222", "hold", 1700000200))

	got, err := parseListing(strings.NewReader(b.String()), 10)
	if err != nil {
		t.Fatalf("un mensaje gigante no puede romper el listado: %v", err)
	}
	if got.Total != 3 || got.Counts["deferred"] != 2 || got.Counts["hold"] != 1 {
		t.Fatalf("conteo: %+v", got)
	}
	if got.OldestArrival != 1700000100 {
		t.Fatalf("el mas antiguo es el gigante: %d", got.OldestArrival)
	}
	var giant *Message
	for i := range got.Items {
		if got.Items[i].QueueID == "GIGANTE0001" {
			giant = &got.Items[i]
		}
	}
	if giant == nil || giant.QueueName != "deferred" || giant.MessageSize != 777 || !giant.RecipientsCapped {
		t.Fatalf("el mensaje gigante debe aparecer para poder actuar sobre el: %+v", giant)
	}
}

func TestLosTextosDeUnMensajeSeAcotan(t *testing.T) {
	long := strings.Repeat("a", 5000)
	line := fmt.Sprintf(`{"queue_name": "deferred", "queue_id": "ABCDEF1234", "arrival_time": 1, "message_size": 1, "sender": "%s@x.y", "recipients": [{"address": "%s@x.y", "delay_reason": "r"}]}`+"\n", long, long)
	got, err := parseListing(strings.NewReader(line), 5)
	if err != nil {
		t.Fatal(err)
	}
	m := got.Items[0]
	if len([]rune(m.Sender)) > maxAddress || len([]rune(m.Recipients[0].Address)) > maxAddress {
		t.Fatalf("remitente de %d y destinatario de %d caracteres: sin tope", len(m.Sender), len(m.Recipients[0].Address))
	}
}

// Mas alla del limite solo hacen falta la cola, el identificador y la llegada: no se decodifican los
// destinatarios de los millones de mensajes que no se devuelven. Un mensaje contado sin decodificar da lo
// mismo que uno decodificado.
func TestContarSinDecodificarDaLoMismoQueDecodificar(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		queue := []string{"deferred", "active", "hold", "incoming"}[i%4]
		b.WriteString(normalLine(fmt.Sprintf("Q%09d", i), queue, int64(1700000000+i*7)))
	}
	b.WriteString(`{"queue_id": "SINCOLA123", "queue_name": "deferred", "arrival_time": 5, "message_size": 1, "sender": "", "recipients": []}` + "\n")
	full, err := parseListing(strings.NewReader(b.String()), 1000)
	if err != nil {
		t.Fatal(err)
	}
	lean, err := parseListing(strings.NewReader(b.String()), 1)
	if err != nil {
		t.Fatal(err)
	}
	if full.Total != lean.Total || full.OldestArrival != lean.OldestArrival || fmt.Sprint(full.Counts) != fmt.Sprint(lean.Counts) {
		t.Fatalf("completo %+v, sin decodificar %+v", full, lean)
	}
	if len(lean.Items) != 1 || !lean.Truncated {
		t.Fatalf("limite: %+v", lean)
	}
}

// Quien pueda abrir conexiones al puerto del agente no debe poder agotar sus descriptores ni sus
// goroutines dejandolas colgadas: el agente corre como root en el contenedor de Postfix.
func TestElAgenteLimitaLasConexionesSimultaneas(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newLimitListener(base, 2)
	defer ln.Close()
	accepted := make(chan net.Conn, 10)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	var clients []net.Conn
	for i := 0; i < 4; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()
	var held []net.Conn
	deadline := time.After(500 * time.Millisecond)
loop:
	for {
		select {
		case c := <-accepted:
			held = append(held, c)
		case <-deadline:
			break loop
		}
	}
	if len(held) != 2 {
		t.Fatalf("se aceptaron %d conexiones simultaneas, tope 2", len(held))
	}
	held[0].Close()
	select {
	case c := <-accepted:
		held = append(held, c)
	case <-time.After(2 * time.Second):
		t.Fatal("al cerrarse una conexion debe aceptarse la siguiente")
	}
	for _, c := range held {
		c.Close()
	}
}

func TestElAgenteSoloHablaTLS13(t *testing.T) {
	cfg := serverTLSConfig(&certReloader{})
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("version minima %x, quiero TLS 1.3: el unico cliente es mail-security (Go)", cfg.MinVersion)
	}
}

// Un flujo de peticiones sin clave no puede llenar el registro del contenedor de Postfix: se anota la
// primera y un resumen cada intervalo.
func TestLasPeticionesSinClaveNoInundanElRegistro(t *testing.T) {
	var logs bytes.Buffer
	srv := NewServer(&fakeOps{}, testKey, slog.New(slog.NewTextHandler(&logs, nil)))
	h := srv.Routes()
	for i := 0; i < 500; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/queue", nil)
		req.Header.Set("Authorization", "Bearer incorrecta")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("estado %d", rec.Code)
		}
	}
	if lines := strings.Count(logs.String(), "\n"); lines > 5 {
		t.Fatalf("500 peticiones sin clave escribieron %d lineas de registro", lines)
	}
}

// `postqueue -j` recorre todos los ficheros de la cola: con millones de mensajes no puede competir con
// la entrega de Postfix.
func TestElListadoCorreConPrioridadMinima(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("solo Linux")
	}
	dir := t.TempDir()
	niceFile := filepath.Join(dir, "nice")
	script := "#!/bin/sh\nsleep 0.5\nnice > " + niceFile + "\nprintf '%s\\n' '" + strings.TrimSpace(normalLine("ABCDEF1234", "deferred", 5)) + "'\n"
	pq := filepath.Join(dir, "postqueue")
	if err := os.WriteFile(pq, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	q := &Queue{postqueue: pq, postsuper: "/bin/false"}
	got, err := q.List(context.Background(), 5)
	if err != nil || got.Total != 1 {
		t.Fatalf("listado: %+v %v", got, err)
	}
	raw, err := os.ReadFile(niceFile)
	if err != nil || strings.TrimSpace(string(raw)) != "19" {
		t.Fatalf("prioridad de postqueue -j: %q %v", raw, err)
	}
}

// Si el contenedor de Postfix se queda sin memoria, el nucleo debe matar al agente (auxiliar y de cara a
// la red) antes que a Postfix.
func TestElAgenteSeOfreceAlNucleoComoPrimerCandidatoDeOOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oom_score_adj")
	if err := os.WriteFile(path, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := preferOOMVictim(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) != "1000" {
		t.Fatalf("oom_score_adj = %q %v", raw, err)
	}
}
