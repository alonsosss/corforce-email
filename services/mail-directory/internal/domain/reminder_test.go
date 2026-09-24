package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validSnooze() *Reminder {
	return &Reminder{
		Kind: ReminderSnooze, MessageID: " abc.123@acme.test ", Folder: "Snoozed", UIDValidity: 7, UID: 42, ReturnFolder: "Clientes/2030",
		DueAt: schedNow.Add(time.Hour), Subject: " Factura ", Addresses: []string{" proveedor@otro.example "},
	}
}

func TestRecordatorioValido(t *testing.T) {
	r := validSnooze()
	r.DueAt = r.DueAt.In(time.FixedZone("Lima", -5*3600))
	r.Status, r.Result, r.Attempts = ReminderDone, ReminderReturned, 3
	if err := r.Normalize(schedNow); err != nil {
		t.Fatal(err)
	}
	if r.Status != ReminderPending || r.Result != "" || r.Attempts != 0 || r.MessageID != "abc.123@acme.test" || r.Subject != "Factura" ||
		r.Addresses[0] != "proveedor@otro.example" || r.DueAt.Location() != time.UTC {
		t.Fatalf("normalizado: %+v", r)
	}
	sinID := validSnooze()
	sinID.MessageID = ""
	if err := sinID.Normalize(schedNow); err != nil {
		t.Fatalf("un pospuesto sin Message-ID se localiza por su UID: %v", err)
	}
	f := &Reminder{Kind: ReminderFollowUp, MessageID: "x@acme.test", Folder: "Sent", DueAt: schedNow.Add(24 * time.Hour)}
	if err := f.Normalize(schedNow); err != nil || f.Addresses == nil {
		t.Fatalf("seguimiento sin UID ni direcciones: %+v %v", f, err)
	}
}

func TestRecordatorioRechazaConCampo(t *testing.T) {
	casos := map[string]struct {
		cambio func(*Reminder)
		campo  string
	}{
		"tipo":                 {func(r *Reminder) { r.Kind = "" }, "kind"},
		"message-id espacio":   {func(r *Reminder) { r.MessageID = "a b@c" }, "message_id"},
		"message-id largo":     {func(r *Reminder) { r.MessageID = strings.Repeat("a", MaxScheduledMessageID+1) }, "message_id"},
		"carpeta comodin":      {func(r *Reminder) { r.Folder = "Snoo%" }, "folder"},
		"uid sin uidvalidity":  {func(r *Reminder) { r.UIDValidity = 0 }, "uid"},
		"pospuesto sin uid":    {func(r *Reminder) { r.UID, r.UIDValidity = 0, 0 }, "uid"},
		"pospuesto sin vuelta": {func(r *Reminder) { r.ReturnFolder = "" }, "return_folder"},
		"vuelta con control":   {func(r *Reminder) { r.ReturnFolder = "a\x00" }, "return_folder"},
		"hora pasada":          {func(r *Reminder) { r.DueAt = schedNow.Add(-2 * time.Minute) }, "due_at"},
		"hora lejana":          {func(r *Reminder) { r.DueAt = schedNow.AddDate(0, 0, MaxReminderDays+1) }, "due_at"},
		"asunto control":       {func(r *Reminder) { r.Subject = "a\nb" }, "subject"},
		"direcciones":          {func(r *Reminder) { r.Addresses = make([]string, MaxReminderAddresses+1) }, "addresses"},
		"direccion invalida":   {func(r *Reminder) { r.Addresses = []string{"a,b@c"} }, "addresses[0]"},
		"seguimiento sin id":   {func(r *Reminder) { r.Kind, r.MessageID, r.ReturnFolder = ReminderFollowUp, "", "" }, "message_id"},
		"seguimiento y vuelta": {func(r *Reminder) { r.Kind = ReminderFollowUp }, "return_folder"},
	}
	for nombre, c := range casos {
		r := validSnooze()
		c.cambio(r)
		var fe *FieldError
		if err := r.Normalize(schedNow); !errors.As(err, &fe) || fe.Field != c.campo {
			t.Fatalf("%s: %v", nombre, err)
		}
	}
	tolerada := validSnooze()
	tolerada.DueAt = schedNow.Add(-30 * time.Second)
	if err := tolerada.Normalize(schedNow); err != nil {
		t.Fatalf("una hora apenas vencida se admite: %v", err)
	}
}

func TestCierreDeRecordatorio(t *testing.T) {
	running := &Reminder{Kind: ReminderSnooze, Status: ReminderRunning, Attempts: 1}
	if _, err := (&Reminder{Status: ReminderPending}).Close(ReminderOutcome{Status: ReminderDone, Result: ReminderMissing}); !errors.Is(err, ErrReminderNotClaimed) {
		t.Fatalf("cerrar uno no reclamado: %v", err)
	}
	tr, err := running.Close(ReminderOutcome{Status: ReminderFailed, Retry: true})
	if err != nil || tr.Status != ReminderPending || tr.RetryAfter != time.Minute {
		t.Fatalf("reintento: %+v %v", tr, err)
	}
	running.Attempts = MaxReminderAttempts
	if tr, _ := running.Close(ReminderOutcome{Status: ReminderFailed, Retry: true}); tr.Status != ReminderFailed {
		t.Fatalf("sin intentos: %+v", tr)
	}
	if _, err := running.Close(ReminderOutcome{Status: ReminderDone, Result: ReminderReplied}); err == nil {
		t.Fatal("un pospuesto no termina como replied")
	}
	if tr, err := running.Close(ReminderOutcome{Status: ReminderDone, Result: ReminderMissing}); err != nil || tr.Result != ReminderMissing {
		t.Fatalf("missing vale para los dos tipos: %+v %v", tr, err)
	}
}

func TestResultadoDeRecordatorio(t *testing.T) {
	validos := []ReminderOutcome{
		{Status: ReminderDone, Result: ReminderReturned},
		{Status: ReminderFailed, Error: "x", Retry: true},
		{Status: ReminderCanceled},
	}
	for _, o := range validos {
		if _, err := NormalizeReminderOutcome(o); err != nil {
			t.Fatalf("%+v: %v", o, err)
		}
	}
	invalidos := map[string]ReminderOutcome{
		"status": {Status: ReminderRunning},
		"result": {Status: ReminderDone, Result: "otro"},
		"retry":  {Status: ReminderDone, Result: ReminderMissing, Retry: true},
	}
	for campo, o := range invalidos {
		var fe *FieldError
		if _, err := NormalizeReminderOutcome(o); !errors.As(err, &fe) || fe.Field != campo {
			t.Fatalf("%s: %v", campo, err)
		}
	}
	if _, err := NormalizeReminderOutcome(ReminderOutcome{Status: ReminderFailed, Result: ReminderMissing}); err == nil {
		t.Fatal("un failed no lleva resultado")
	}
	o, _ := NormalizeReminderOutcome(ReminderOutcome{Status: ReminderFailed, Error: "linea\r\nsegunda\t" + strings.Repeat("x", 2000)})
	if strings.ContainsAny(o.Error, "\r\n\t") || len([]rune(o.Error)) > MaxScheduledErrorRunes {
		t.Fatalf("error sin limpiar: %q", o.Error)
	}
}

func TestRespuestaRapidaValida(t *testing.T) {
	q := &QuickReply{Name: " Cita \t confirmada ", HTML: " <p>Hola {nombre}</p> ", Text: "Hola {nombre}\r\nSaludos"}
	if err := q.Normalize(); err != nil || q.Name != "Cita confirmada" || q.HTML != "<p>Hola {nombre}</p>" || q.Text != "Hola {nombre}\nSaludos" {
		t.Fatalf("%+v %v", q, err)
	}
	casos := map[string]*QuickReply{
		"name": {Name: "  ", Text: "x"},
		"html": {Name: "Vacia"},
		"text": {Name: "Control", Text: "a\x01"},
	}
	for campo, q := range casos {
		var fe *FieldError
		if err := q.Normalize(); !errors.As(err, &fe) || fe.Field != campo {
			t.Fatalf("%s: %v", campo, err)
		}
	}
	if err := (&QuickReply{Name: strings.Repeat("ñ", MaxQuickReplyNameRunes), Text: "x"}).Normalize(); err != nil {
		t.Fatalf("el tope del nombre cuenta caracteres, no bytes: %v", err)
	}
}
