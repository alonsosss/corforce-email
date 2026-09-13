package domain

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestValidateIdempotencyKey(t *testing.T) {
	for _, ok := range []string{"0f6a6f8e-3d1b-4b9e-9a55-6d1c2b3a4f5e", strings.Repeat("a", 16), strings.Repeat("Z_-9", 32)} {
		if err := ValidateIdempotencyKey(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	var verr *ValidationError
	for _, bad := range []string{"", "corta", strings.Repeat("a", 129), "con espacio y mas texto", "clave\r\nX-Inyectada: 1"} {
		if err := ValidateIdempotencyKey(bad); !errors.As(err, &verr) || verr.Field != "idempotency_key" {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestNewPartSource(t *testing.T) {
	src, err := NewPartSource("INBOX/Proyectos", 9, []string{"2", "1.2"})
	if err != nil || src.Folder != "INBOX/Proyectos" || src.UID != 9 || strings.Join(src.Parts, ",") != "2,1.2" {
		t.Fatalf("%+v %v", src, err)
	}
	many := make([]string, MaxAttachments+1)
	for i := range many {
		many[i] = strconv.Itoa(i + 1)
	}
	cases := []struct {
		name   string
		folder string
		uid    uint32
		parts  []string
		field  string
	}{
		{"carpeta con CRLF", "INBOX\r\nA1 DELETE INBOX", 9, []string{"2"}, "source_folder"},
		{"carpeta vacia", "", 9, []string{"2"}, "source_folder"},
		{"sin UID", "INBOX", 0, []string{"2"}, "source_uid"},
		{"sin partes", "INBOX", 9, nil, "source_parts"},
		{"parte invalida", "INBOX", 9, []string{"1..2"}, "source_parts"},
		{"parte repetida", "INBOX", 9, []string{"2", "2"}, "source_parts"},
		{"demasiadas", "INBOX", 9, many, "source_parts"},
	}
	var verr *ValidationError
	for _, c := range cases {
		if _, err := NewPartSource(c.folder, c.uid, c.parts); !errors.As(err, &verr) || verr.Field != c.field {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestLasPartesPendientesCuentanComoAdjuntos(t *testing.T) {
	d := Draft{From: Address{Email: "ana@empresa.pe"}, To: []Address{{Email: "a@x.com"}}}
	for i := 0; i < MaxAttachments; i++ {
		d.Attachments = append(d.Attachments, Attachment{Filename: "a.txt", Data: []byte("x")})
	}
	if err := d.ValidateForSend(Limits{MaxRecipients: 10, MaxMessageBytes: 1 << 20}); err != nil {
		t.Fatalf("en el tope: %v", err)
	}
	d.Source = &PartSource{Folder: "INBOX", UID: 1, Parts: []string{"2"}}
	var verr *ValidationError
	if err := d.ValidateForSend(Limits{MaxRecipients: 10, MaxMessageBytes: 1 << 20}); !errors.As(err, &verr) || verr.Field != "attachments" {
		t.Fatalf("una parte del buzon mas pasa el tope: %v", err)
	}
}
