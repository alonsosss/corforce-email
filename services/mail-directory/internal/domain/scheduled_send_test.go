package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var schedNow = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

func validScheduled() *ScheduledSend {
	return &ScheduledSend{
		MessageID: "<abc.123@acme.test>", Folder: "Scheduled", UIDValidity: 7, UID: 42,
		SendAt: schedNow.Add(time.Hour), Subject: " Propuesta ", Recipients: []string{" cliente@otro.example ", "José@correo.example"},
	}
}

func TestEnvioProgramadoValido(t *testing.T) {
	s := validScheduled()
	s.SendAt = s.SendAt.In(time.FixedZone("Lima", -5*3600))
	if err := s.Normalize(schedNow); err != nil {
		t.Fatal(err)
	}
	if s.Status != ScheduledPending || s.Attempts != 0 || s.Subject != "Propuesta" || s.Recipients[0] != "cliente@otro.example" || s.SendAt.Location() != time.UTC {
		t.Fatalf("normalizado: %+v", s)
	}
}

func TestEnvioProgramadoRechazaConCampo(t *testing.T) {
	casos := map[string]struct {
		cambio func(*ScheduledSend)
		campo  string
	}{
		"sin message-id":     {func(s *ScheduledSend) { s.MessageID = " " }, "message_id"},
		"message-id espacio": {func(s *ScheduledSend) { s.MessageID = "<a b@c>" }, "message_id"},
		"carpeta comodin":    {func(s *ScheduledSend) { s.Folder = "Sched*" }, "folder"},
		"carpeta vacia":      {func(s *ScheduledSend) { s.Folder = "" }, "folder"},
		"sin uidvalidity":    {func(s *ScheduledSend) { s.UIDValidity = 0 }, "uid_validity"},
		"sin uid":            {func(s *ScheduledSend) { s.UID = 0 }, "uid"},
		"hora pasada":        {func(s *ScheduledSend) { s.SendAt = schedNow.Add(-2 * time.Minute) }, "send_at"},
		"sin hora":           {func(s *ScheduledSend) { s.SendAt = time.Time{} }, "send_at"},
		"demasiado lejos":    {func(s *ScheduledSend) { s.SendAt = schedNow.AddDate(0, 0, MaxScheduledDays+1) }, "send_at"},
		"asunto con salto":   {func(s *ScheduledSend) { s.Subject = "a\r\nBcc: x@y" }, "subject"},
		"asunto largo":       {func(s *ScheduledSend) { s.Subject = strings.Repeat("a", MaxScheduledSubjectRunes+1) }, "subject"},
		"sin destinatarios":  {func(s *ScheduledSend) { s.Recipients = nil }, "recipients"},
		"destinatario raro":  {func(s *ScheduledSend) { s.Recipients = []string{"a@b.example", "Nombre <x@y>"} }, "recipients[1]"},
		"destinatario sin @": {func(s *ScheduledSend) { s.Recipients = []string{"nadie"} }, "recipients[0]"},
		"muchos destinatarios": {func(s *ScheduledSend) {
			s.Recipients = make([]string, MaxScheduledRecipients+1)
		}, "recipients"},
	}
	for nombre, c := range casos {
		s := validScheduled()
		c.cambio(s)
		if got := fieldOf(t, s.Normalize(schedNow)); got != c.campo {
			t.Errorf("%s: campo %q, se esperaba %q", nombre, got, c.campo)
		}
	}
	s := validScheduled()
	s.SendAt = schedNow.Add(-30 * time.Second)
	if err := s.Normalize(schedNow); err != nil {
		t.Errorf("una hora apenas vencida se admite (sale en la siguiente reclamacion): %v", err)
	}
}

func TestCierreConReintentoCrecienteHastaElMaximo(t *testing.T) {
	retry := ScheduledOutcome{Status: ScheduledFailed, Error: "421 intente luego", Retry: true}
	var waits []time.Duration
	for attempt := 1; attempt <= MaxScheduledAttempts; attempt++ {
		s := &ScheduledSend{Status: ScheduledSending, Attempts: attempt}
		tr, err := s.Close(retry)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < MaxScheduledAttempts {
			if tr.Status != ScheduledPending {
				t.Fatalf("intento %d: %+v", attempt, tr)
			}
			waits = append(waits, tr.RetryAfter)
			continue
		}
		if tr.Status != ScheduledFailed || tr.RetryAfter != 0 {
			t.Fatalf("el ultimo intento queda failed: %+v", tr)
		}
	}
	for i := 1; i < len(waits); i++ {
		if waits[i] <= waits[i-1] {
			t.Fatalf("la espera no crece: %v", waits)
		}
	}
	if waits[0] != time.Minute {
		t.Fatalf("primera espera: %v", waits[0])
	}
	s := &ScheduledSend{Status: ScheduledSending, Attempts: 1}
	if tr, _ := s.Close(ScheduledOutcome{Status: ScheduledFailed}); tr.Status != ScheduledFailed {
		t.Fatalf("sin reintento es final: %+v", tr)
	}
	if tr, _ := s.Close(ScheduledOutcome{Status: ScheduledSent}); tr.Status != ScheduledSent {
		t.Fatalf("enviado: %+v", tr)
	}
	pending := &ScheduledSend{Status: ScheduledPending}
	if _, err := pending.Close(ScheduledOutcome{Status: ScheduledSent}); !errors.Is(err, ErrScheduledSendNotClaimed) {
		t.Fatalf("una fila no reclamada no se cierra: %v", err)
	}
}

func TestResultadoDelTrabajador(t *testing.T) {
	if _, err := NormalizeOutcome(ScheduledOutcome{Status: "pending"}); fieldOf(t, err) != "status" {
		t.Fatal("estado no final")
	}
	if _, err := NormalizeOutcome(ScheduledOutcome{Status: ScheduledSent, Retry: true}); fieldOf(t, err) != "retry" {
		t.Fatal("reintento de un enviado")
	}
	o, err := NormalizeOutcome(ScheduledOutcome{Status: ScheduledFailed, Error: "550 5.1.1\r\n  usuario\x00 desconocido\xff" + strings.Repeat("x", 2000)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(o.Error, "\r\n\x00") || !strings.HasPrefix(o.Error, "550 5.1.1 usuario desconocido") || len([]rune(o.Error)) != MaxScheduledErrorRunes {
		t.Fatalf("error limpio y recortado: %q", o.Error[:60])
	}
}

func TestParametrosDeReclamacion(t *testing.T) {
	p, err := NormalizeClaim(0, 0)
	if err != nil || p.Limit != DefaultScheduledClaim || p.Lease != DefaultScheduledLease {
		t.Fatalf("por defecto: %+v %v", p, err)
	}
	for _, c := range []struct{ limit, lease int }{{-1, 0}, {MaxScheduledClaim + 1, 0}, {1, 10}, {1, 3601}, {1, -5}, {1, 1 << 62}} {
		if _, err := NormalizeClaim(c.limit, c.lease); err == nil {
			t.Errorf("%+v deberia rechazarse", c)
		}
	}
}
