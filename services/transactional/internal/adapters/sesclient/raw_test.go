package sesclient

import (
	"testing"

	"github.com/alonsosss/corforce-email/services/transactional/internal/domain"
	"github.com/google/uuid"
)

// Un mensaje de SMTP sale con contenido Raw: su MIME tal cual, el sobre como destinatarios y las
// mismas etiquetas y el mismo configuration set que cualquier otro envio del carril.
func TestBuildInputRaw(t *testing.T) {
	email := domain.OutgoingEmail{
		MessageID: uuid.New(), TenantID: uuid.New(), From: "Tienda <no-reply@shop.example.com>",
		To: []string{"ana@example.com", "oculto@example.com"}, Subject: "no se usa", HTML: "<p>no se usa</p>",
		Tags: map[string]string{"origen": "tienda", "tenant_id": "suplantado"},
		Raw:  []byte("From: no-reply@shop.example.com\r\nSubject: Pedido\r\n\r\nGracias\r\n"),
	}
	in := BuildInput(email, "cfm-transactional")
	if in.Content.Raw == nil || string(in.Content.Raw.Data) != string(email.Raw) || in.Content.Simple != nil {
		t.Fatalf("contenido Raw: %+v", in.Content)
	}
	if len(in.Destination.ToAddresses) != 2 || len(in.Destination.CcAddresses) != 0 || len(in.Destination.BccAddresses) != 0 {
		t.Fatalf("sobre: %+v", in.Destination)
	}
	if *in.FromEmailAddress != email.From || *in.ConfigurationSetName != "cfm-transactional" {
		t.Fatalf("remitente y carril: %v %v", *in.FromEmailAddress, *in.ConfigurationSetName)
	}
	tags := map[string]string{}
	for _, tg := range in.EmailTags {
		tags[*tg.Name] = *tg.Value
	}
	if tags["tenant_id"] != email.TenantID.String() || tags["message_id"] != email.MessageID.String() || tags["origen"] != "tienda" || len(tags) != 3 {
		t.Fatalf("etiquetas de atribucion: %v", tags)
	}
}
