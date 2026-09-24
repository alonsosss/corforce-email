package domain

import (
	"strings"
	"testing"
)

func TestFirma(t *testing.T) {
	s := &MailboxSignature{Enabled: true, HTML: "  <p>Ana</p>\n", Text: "Ana\r\nVentas "}
	if err := s.Normalize(); err != nil {
		t.Fatal(err)
	}
	if s.HTML != "<p>Ana</p>" || s.Text != "Ana\nVentas" {
		t.Fatalf("normalizada: %+v", s)
	}
	if err := (&MailboxSignature{}).Normalize(); err != nil {
		t.Fatalf("una firma apagada y vacia es valida: %v", err)
	}
	casos := map[string]struct {
		s     MailboxSignature
		campo string
	}{
		"activa vacia": {MailboxSignature{Enabled: true, HTML: " "}, "html"},
		"html enorme":  {MailboxSignature{HTML: strings.Repeat("a", MaxSignatureHTMLBytes+1)}, "html"},
		"texto enorme": {MailboxSignature{Text: strings.Repeat("a", MaxSignatureTextBytes+1)}, "text"},
		"nulo":         {MailboxSignature{HTML: "a\x00b"}, "html"},
		"utf8 roto":    {MailboxSignature{Text: "a\xffb"}, "text"},
		"escape ansi":  {MailboxSignature{Text: "a\x1b[31mb"}, "text"},
	}
	for nombre, c := range casos {
		s := c.s
		if got := fieldOf(t, s.Normalize()); got != c.campo {
			t.Errorf("%s: campo %q, se esperaba %q", nombre, got, c.campo)
		}
	}
}
