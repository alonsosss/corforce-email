package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleQueue = `{"queue_name": "deferred", "queue_id": "4Xy1Zk2hSBzXyZ", "arrival_time": 1700000000, "message_size": 1234, "forced_expire": false, "sender": "ana@acme.test", "recipients": [{"address": "x@ejemplo.org", "delay_reason": "connect to ejemplo.org[192.0.2.1]:25: Connection timed out"}, {"address": "y@ejemplo.org"}]}
{"queue_name": "hold", "queue_id": "B7C2D9E1F0", "arrival_time": 1700000100, "message_size": 99, "sender": "", "recipients": [{"address": "z@ejemplo.net"}]}
{"queue_name": "active", "queue_id": "no valido!", "arrival_time": 1, "message_size": 1, "sender": "a@b.c", "recipients": []}
`

func TestElListadoConservaLoQueSeMuestraYDescartaLoDemas(t *testing.T) {
	got, err := parseListing(strings.NewReader(sampleQueue), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || got.Truncated || len(got.Items) != 2 {
		t.Fatalf("una linea con un identificador invalido no cuenta: %+v", got)
	}
	first := got.Items[0]
	if first.QueueID != "4Xy1Zk2hSBzXyZ" || first.QueueName != "deferred" || first.MessageSize != 1234 || first.Sender != "ana@acme.test" ||
		first.RecipientsTotal != 2 || first.Recipients[0].DelayReason == "" || first.Recipients[1].DelayReason != "" {
		t.Fatalf("primer mensaje: %+v", first)
	}
	if got.Items[1].Sender != "" || got.Items[1].QueueName != "hold" {
		t.Fatalf("un remitente vacio (rebote) es valido: %+v", got.Items[1])
	}
}

func TestElListadoSeAcotaPeroCuentaTodo(t *testing.T) {
	got, err := parseListing(strings.NewReader(sampleQueue), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 2 || !got.Truncated || len(got.Items) != 1 {
		t.Fatalf("total y truncado: %+v", got)
	}
}

func TestUnMensajeConMuchosDestinatariosDevuelveUnTopeYSuTotal(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"queue_name":"deferred","queue_id":"ABCDEF1234","arrival_time":1,"message_size":1,"sender":"a@b.c","recipients":[`)
	for i := 0; i < maxRecipients+20; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"address":"d@e.f","delay_reason":"` + strings.Repeat("ñ", maxDelayReason+50) + `"}`)
	}
	b.WriteString("]}\n")
	got, err := parseListing(strings.NewReader(b.String()), 5)
	if err != nil {
		t.Fatal(err)
	}
	m := got.Items[0]
	if len(m.Recipients) != maxRecipients || m.RecipientsTotal != maxRecipients+20 || !m.RecipientsCapped {
		t.Fatalf("destinatarios: %d de %d capped=%v", len(m.Recipients), m.RecipientsTotal, m.RecipientsCapped)
	}
	if n := len([]rune(m.Recipients[0].DelayReason)); n != maxDelayReason {
		t.Fatalf("el motivo se recorta a %d caracteres y llego %d", maxDelayReason, n)
	}
}

func TestUnaLineaIlegibleEsUnError(t *testing.T) {
	if _, err := parseListing(strings.NewReader("no es json\n"), 5); !errors.Is(err, ErrCommand) {
		t.Fatalf("se esperaba ErrCommand: %v", err)
	}
}

func TestSoloSeAceptanIdentificadoresDeCola(t *testing.T) {
	for _, ok := range []string{"4Xy1Zk2hSBzXyZ", "B7C2D9E1F0", "ABCDE"} {
		if !ValidQueueID(ok) {
			t.Errorf("%q debe ser valido", ok)
		}
	}
	for _, bad := range []string{"", "ABCD", strings.Repeat("A", 26), "ABC DEF12", "-d", "ALL", "A;rm -rf", "../etc/pass", "ABCDE\n", "ABC-DE123", "ÁBCDE12345"} {
		if ValidQueueID(bad) {
			t.Errorf("%q no debe ser valido", bad)
		}
	}
}

// fakeBinary escribe un ejecutable que anota sus argumentos y responde lo que se le diga.
func fakeBinary(t *testing.T, dir, name, output string, exit int) (path, log string) {
	t.Helper()
	path, log = filepath.Join(dir, name), filepath.Join(dir, name+".args")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\nprintf '%s' '" + output + "'\nexit " + itoa(exit) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, log
}

func itoa(n int) string { return string(rune('0' + n)) }

func args(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func TestCadaAccionEjecutaSuComandoConElIdentificadorComoUnicoArgumento(t *testing.T) {
	dir := t.TempDir()
	pq, pqLog := fakeBinary(t, dir, "postqueue", "", 0)
	ps, psLog := fakeBinary(t, dir, "postsuper", "postsuper: Done: 1 message", 0)
	q := &Queue{postqueue: pq, postsuper: ps}
	ctx := context.Background()

	cases := []struct {
		action Action
		log    string
		want   string
	}{
		{ActionRetry, pqLog, "-i 4Xy1Zk2hSBzXyZ"},
		{ActionHold, psLog, "-h 4Xy1Zk2hSBzXyZ"},
		{ActionUnhold, psLog, "-H 4Xy1Zk2hSBzXyZ"},
		{ActionDelete, psLog, "-d 4Xy1Zk2hSBzXyZ"},
	}
	for _, c := range cases {
		_ = os.Remove(c.log)
		if err := q.Apply(ctx, c.action, "4Xy1Zk2hSBzXyZ"); err != nil {
			t.Fatalf("%s: %v", c.action, err)
		}
		if got := args(t, c.log); got != c.want {
			t.Errorf("%s ejecuto %q, se esperaba %q", c.action, got, c.want)
		}
	}
}

func TestUnaAccionInvalidaNoEjecutaNada(t *testing.T) {
	dir := t.TempDir()
	pq, pqLog := fakeBinary(t, dir, "postqueue", "", 0)
	ps, psLog := fakeBinary(t, dir, "postsuper", "", 0)
	q := &Queue{postqueue: pq, postsuper: ps}
	for _, c := range []struct {
		action Action
		id     string
	}{{ActionDelete, "-d ALL"}, {ActionDelete, "ABC;reboot"}, {ActionDelete, ""}, {Action("super_delete"), "ABCDEF1234"}, {Action(""), "ABCDEF1234"}} {
		if err := q.Apply(context.Background(), c.action, c.id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s %q: %v", c.action, c.id, err)
		}
	}
	if args(t, pqLog) != "" || args(t, psLog) != "" {
		t.Fatal("no debe ejecutarse ningun comando")
	}
}

func TestUnMensajeQueYaNoEstaEsNotFoundYUnFalloDePostfixEsErrCommand(t *testing.T) {
	dir := t.TempDir()
	ps, _ := fakeBinary(t, dir, "postsuper", "postsuper: Deleted: 0 messages", 0)
	q := &Queue{postqueue: "/bin/true", postsuper: ps}
	if err := q.Apply(context.Background(), ActionDelete, "ABCDEF1234"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("0 mensajes afectados: %v", err)
	}
	ps, _ = fakeBinary(t, dir, "postsuper2", "postsuper: fatal", 1)
	q.postsuper = ps
	if err := q.Apply(context.Background(), ActionDelete, "ABCDEF1234"); !errors.Is(err, ErrCommand) {
		t.Fatalf("codigo de salida distinto de cero: %v", err)
	}
}

func TestListaLeeLaSalidaDePostqueue(t *testing.T) {
	dir := t.TempDir()
	pq := filepath.Join(dir, "postqueue")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" != \"-j\" ]; then exit 9; fi\n" +
		"cat <<'EOF'\n" + sampleQueue + "EOF\n"
	if err := os.WriteFile(pq, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := (&Queue{postqueue: pq}).List(context.Background(), 10)
	if err != nil || got.Total != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := (&Queue{postqueue: "/bin/false"}).List(context.Background(), 10); !errors.Is(err, ErrCommand) {
		t.Fatalf("un postqueue que falla es ErrCommand: %v", err)
	}
}
