package app

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func simpleDraft() domain.Draft {
	return domain.Draft{To: []domain.Address{{Email: "luis@x.com"}}, Subject: "Hola", Text: "cuerpo"}
}

func TestSendExigeClaveDeIdempotencia(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	var verr *domain.ValidationError
	for _, key := range []string{"", "corta", strings.Repeat("a", 129), "clave con espacios!!"} {
		_, err := h.svc.Send(ctx, sess, simpleDraft(), domain.SendOptions{IdempotencyKey: key})
		if !errors.As(err, &verr) || verr.Field != "idempotency_key" {
			t.Fatalf("%q: %v", key, err)
		}
	}
	if len(h.sender.calls) != 0 || len(h.ledger.records) != 0 {
		t.Fatal("sin clave valida no se reserva ni se envia nada")
	}
}

func TestSendRepetidoDevuelveElResultadoSinReenviar(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	opts := sendOpts(0)
	first, err := h.svc.Send(ctx, sess, simpleDraft(), opts)
	if err != nil || first.Replayed {
		t.Fatalf("primer envio: %+v %v", first, err)
	}
	again, err := h.svc.Send(ctx, sess, simpleDraft(), opts)
	if err != nil || !again.Replayed || again.MessageID != first.MessageID || !again.SavedToSent {
		t.Fatalf("reintento: %+v %v", again, err)
	}
	if len(h.sender.calls) != 1 || len(h.mb.appended) != 1 {
		t.Fatalf("el reintento no entrega ni guarda de nuevo: envios=%d copias=%d", len(h.sender.calls), len(h.mb.appended))
	}
}

func TestSendRetiraElBorradorTrasGuardarLaCopia(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	res, err := h.svc.Send(ctx, sess, simpleDraft(), sendOpts(5))
	if err != nil || !res.SavedToSent || !res.DraftRemoved {
		t.Fatalf("resultado: %+v %v", res, err)
	}
	if strings.Join(h.mb.expunged, ",") != "Drafts:5" || h.mb.appended[0].folder != "Sent" {
		t.Fatalf("expunged=%v copias=%+v", h.mb.expunged, h.mb.appended)
	}
	if res, _ := h.svc.Send(ctx, sess, simpleDraft(), sendOpts(0)); res.DraftRemoved {
		t.Fatal("sin borrador que retirar, draft_removed es false")
	}
}

func TestSendSinCopiaEnEnviadosConservaElBorrador(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.appendErr = map[string]error{"Sent": fmt.Errorf("%w: disco lleno", domain.ErrUnavailable)}
	res, err := h.svc.Send(ctx, sess, simpleDraft(), sendOpts(5))
	if err != nil {
		t.Fatal(err)
	}
	if res.SavedToSent || res.DraftRemoved || len(h.mb.expunged) != 0 {
		t.Fatalf("sin copia en Enviados el borrador es el unico registro de lo que salio: %+v %v", res, h.mb.expunged)
	}
}

func TestReintentoRetiraElBorradorSinReenviar(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	opts := sendOpts(5)
	h.mb.expungeErr = fmt.Errorf("%w: IMAP caido", domain.ErrUnavailable)
	res, err := h.svc.Send(ctx, sess, simpleDraft(), opts)
	if err != nil || !res.SavedToSent || res.DraftRemoved {
		t.Fatalf("envio con fallo al retirar: %+v %v", res, err)
	}
	h.mb.expungeErr = nil
	res, err = h.svc.Send(ctx, sess, simpleDraft(), opts)
	if err != nil || !res.Replayed || !res.DraftRemoved {
		t.Fatalf("reintento: %+v %v", res, err)
	}
	if len(h.sender.calls) != 1 || strings.Join(h.mb.expunged, ",") != "Drafts:5" {
		t.Fatalf("envios=%d expunged=%v", len(h.sender.calls), h.mb.expunged)
	}
	opened := h.mail.opened
	if res, _ = h.svc.Send(ctx, sess, simpleDraft(), opts); !res.DraftRemoved || h.mail.opened != opened {
		t.Fatal("con el borrador ya retirado, el siguiente reintento no toca el buzon")
	}
}

func TestSendRechazadoLiberaLaClave(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	opts := sendOpts(0)
	h.sender.err = fmt.Errorf("550 5.1.1: %w", &domain.RecipientRejectedError{Address: "luis@x.com"})
	if _, err := h.svc.Send(ctx, sess, simpleDraft(), opts); err == nil {
		t.Fatal("se esperaba el rechazo")
	}
	h.sender.err = nil
	res, err := h.svc.Send(ctx, sess, simpleDraft(), opts)
	if err != nil || res.Replayed || len(h.sender.calls) != 2 {
		t.Fatalf("el mensaje no salio: la misma clave lo puede reintentar: %+v %v", res, err)
	}
}

func TestSendInciertoNoSeReintentaSolo(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	opts := sendOpts(0)
	h.sender.err = fmt.Errorf("%w: conexion cortada tras el punto final", domain.ErrDeliveryUncertain)
	if _, err := h.svc.Send(ctx, sess, simpleDraft(), opts); !errors.Is(err, domain.ErrDeliveryUncertain) {
		t.Fatalf("got %v", err)
	}
	h.sender.err = nil
	if _, err := h.svc.Send(ctx, sess, simpleDraft(), opts); !errors.Is(err, domain.ErrDeliveryUncertain) {
		t.Fatalf("la misma clave no reintenta un envio incierto: %v", err)
	}
	if len(h.sender.calls) != 1 || len(h.mb.appended) != 0 {
		t.Fatalf("envios=%d copias=%d", len(h.sender.calls), len(h.mb.appended))
	}
	if _, err := h.svc.Send(ctx, sess, simpleDraft(), sendOpts(0)); err != nil || len(h.sender.calls) != 2 {
		t.Fatalf("reenviar lo decide el usuario con otra clave: %v", err)
	}
}

func TestSendMismaClaveConOtroMensaje(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	opts := sendOpts(0)
	if _, err := h.svc.Send(ctx, sess, simpleDraft(), opts); err != nil {
		t.Fatal(err)
	}
	other := simpleDraft()
	other.Text = "otro cuerpo"
	if _, err := h.svc.Send(ctx, sess, other, opts); !errors.Is(err, domain.ErrIdempotencyKeyReused) {
		t.Fatalf("got %v", err)
	}
	if len(h.sender.calls) != 1 {
		t.Fatal("un mensaje distinto con una clave usada no sale")
	}
}

func TestSendEnCursoConLaMismaClave(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	opts := sendOpts(0)
	d := simpleDraft()
	h.ledger.records[sendKey(testUser, opts.IdempotencyKey)] = domain.SendRecord{
		State: domain.SendPending, Fingerprint: fingerprint(d, 0), Token: "otra-peticion",
	}
	if _, err := h.svc.Send(ctx, sess, d, opts); !errors.Is(err, domain.ErrSendInProgress) {
		t.Fatalf("got %v", err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("con el envio en curso no se entrega otra vez")
	}
	if sendKey("otro@empresa.pe", opts.IdempotencyKey) == sendKey(testUser, opts.IdempotencyKey) {
		t.Fatal("la clave del registro depende del buzon")
	}
}

func TestSendSinRegistroNoEnvia(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.ledger.failReserve = errors.New("redis caido")
	if _, err := h.svc.Send(ctx, sess, simpleDraft(), sendOpts(0)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("sin poder garantizar que no se duplica, no se envia")
	}
}

func TestRemitentesDelDirectorio(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.ids = []string{"ventas@empresa.pe"}
	to := []domain.Address{{Email: "luis@x.com"}}

	_, err := h.svc.Send(ctx, sess, domain.Draft{From: domain.Address{Email: "VENTAS@empresa.pe"}, To: to, Text: "x"}, sendOpts(0))
	if err != nil || h.sender.calls[0].from != "ventas@empresa.pe" || h.composer.last.From.Email != "ventas@empresa.pe" {
		t.Fatalf("remitente permitido: %v %+v", err, h.sender.calls)
	}
	_, err = h.svc.Send(ctx, sess, domain.Draft{From: domain.Address{Email: "gerencia@empresa.pe"}, To: to, Text: "x"}, sendOpts(0))
	if !errors.Is(err, domain.ErrSenderNotAllowed) || len(h.sender.calls) != 1 {
		t.Fatalf("un remitente que Postfix rechazaria no llega a Postfix: %v", err)
	}
	if _, err := h.svc.SaveDraft(ctx, sess, domain.Draft{From: domain.Address{Email: "gerencia@empresa.pe"}}, 0); !errors.Is(err, domain.ErrSenderNotAllowed) {
		t.Fatalf("un borrador tampoco lleva un remitente ajeno: %v", err)
	}

	h.directory.err = fmt.Errorf("%w: mail-directory caido", domain.ErrUnavailable)
	if _, err := h.svc.Send(ctx, sess, domain.Draft{From: domain.Address{Email: "ventas@empresa.pe"}, To: to, Text: "x"}, sendOpts(0)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("sin directorio no se admite un remitente distinto del buzon: %v", err)
	}
	calls := h.directory.calls
	if _, err := h.svc.Send(ctx, sess, simpleDraft(), sendOpts(0)); err != nil || h.directory.calls != calls {
		t.Fatalf("enviar como el propio buzon no depende del directorio: %v", err)
	}
}

func TestIdentitiesBuzonPrimero(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.ids = []string{"ventas@empresa.pe", "ANA@empresa.pe", "ventas@empresa.pe"}
	got, err := h.svc.Identities(ctx, sess)
	want := []domain.SenderIdentity{{Address: testUser, Name: "Ana Perez", Primary: true}, {Address: "ventas@empresa.pe"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v", got, err)
	}
	h.directory.err = fmt.Errorf("%w: mail-directory caido", domain.ErrUnavailable)
	if _, err := h.svc.Identities(ctx, sess); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestMetaSaleDeLaConfiguracion(t *testing.T) {
	h := newHarness(t)
	m := h.svc.Meta()
	checks := []struct {
		name      string
		got, want any
	}{
		{"max_recipients", m.MaxRecipients, 3},
		{"max_message_bytes", m.MaxMessageBytes, int64(1000)},
		{"max_attachments", m.MaxAttachments, domain.MaxAttachments},
		{"max_download_bytes", m.MaxDownloadBytes, int64(1 << 20)},
		{"max_body_part_bytes", m.MaxBodyPartBytes, int64(1 << 20)},
		{"max_subject_chars", m.MaxSubjectChars, domain.MaxSubjectRunes},
		{"max_search_bytes", m.MaxSearchBytes, domain.MaxSearchBytes},
		{"max_folder_name_bytes", m.MaxFolderNameBytes, domain.MaxFolderNameBytes},
		{"default_page_size", m.DefaultPageSize, domain.DefaultPerPage},
		{"max_page_size", m.MaxPageSize, domain.MaxPerPage},
		{"folder_roles", m.FolderRoles, domain.SpecialRoles},
		{"mutable_flags", m.MutableFlags, domain.MutableFlags},
		{"session_idle", m.SessionIdle, 30 * time.Minute},
		{"session_max", m.SessionMax, 12 * time.Hour},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, quiero %v", c.name, c.got, c.want)
		}
	}
	// Lo publicado es lo que se aplica.
	for _, f := range m.MutableFlags {
		if _, err := domain.NewFlagChange([]string{string(f)}, nil); err != nil {
			t.Errorf("%s se publica como modificable y se rechaza: %v", f, err)
		}
	}
	if _, err := domain.NewFlagChange([]string{string(domain.FlagDeleted)}, nil); err == nil {
		t.Error(`\Deleted no es modificable`)
	}
	if q, _ := domain.NewListQuery(1, m.MaxPageSize+1, ""); q.PerPage != m.MaxPageSize {
		t.Errorf("la pagina maxima publicada no es la que se aplica: %d", q.PerPage)
	}
	m.FolderRoles[0] = "otra"
	if domain.SpecialRoles[0] != domain.RoleInbox {
		t.Fatal("el catalogo publicado es una copia")
	}
}

func TestReenvioConAdjuntosDelServidor(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.parts = map[string]storedPart{
		"2":   {part: domain.Part{ID: "2", Filename: "../informe\u202e.pdf", ContentType: `application/pdf; name="x"`}, data: []byte("%PDF")},
		"1.2": {part: domain.Part{ID: "1.2", ContentType: "image/png"}, data: []byte("PNG")},
	}
	d := simpleDraft()
	d.Attachments = []domain.Attachment{{Filename: "nota.txt", ContentType: "text/plain", Data: []byte("hola")}}
	d.Source = &domain.PartSource{Folder: "INBOX", UID: 9, Parts: []string{"2", "1.2"}}
	if _, err := h.svc.Send(ctx, sess, d, sendOpts(0)); err != nil {
		t.Fatal(err)
	}
	got := h.composer.last.Attachments
	if len(got) != 3 || got[1].Filename != "informe.pdf" || got[1].ContentType != "application/pdf" ||
		string(got[1].Data) != "%PDF" || got[2].Filename != "adjunto" || got[2].ContentType != "image/png" {
		t.Fatalf("adjuntos: %+v", got)
	}
	if strings.Join(h.scanner.scanned, ",") != "nota.txt,informe.pdf,adjunto" {
		t.Fatalf("los adjuntos del buzon pasan por ClamAV como los subidos: %v", h.scanner.scanned)
	}
	// Cada parte se pide con lo que le queda al mensaje, no con el tope de descarga.
	reqs := h.mb.partReqs
	if len(reqs) != 2 || reqs[0].folder != "INBOX" || reqs[0].uid != 9 || reqs[0].limit != 1000-14 || reqs[1].limit != 1000-18 {
		t.Fatalf("peticiones de partes: %+v", reqs)
	}
}

func TestReenvioInfectadoNoSale(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.parts = map[string]storedPart{"2": {part: domain.Part{ID: "2", Filename: "virus.exe"}, data: []byte("MZ")}}
	h.scanner.infect = "virus.exe"
	d := simpleDraft()
	d.Source = &domain.PartSource{Folder: "INBOX", UID: 9, Parts: []string{"2"}}
	if _, err := h.svc.Send(ctx, sess, d, sendOpts(0)); !errors.Is(err, domain.ErrAttachmentInfected) {
		t.Fatalf("got %v", err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("un adjunto del buzon con malware no sale")
	}
}

func TestReenvioAcotaNumeroYTamano(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.parts = map[string]storedPart{"2": {part: domain.Part{ID: "2", Filename: "grande.bin"}, data: make([]byte, 2000)}}
	d := simpleDraft()
	d.Source = &domain.PartSource{Folder: "INBOX", UID: 9, Parts: []string{"2"}}
	if _, err := h.svc.Send(ctx, sess, d, sendOpts(0)); !errors.Is(err, domain.ErrMessageTooLarge) {
		t.Fatalf("parte mayor que lo que le queda al mensaje: %v", err)
	}

	opened := h.mail.opened
	many := simpleDraft()
	for i := 0; i < domain.MaxAttachments; i++ {
		many.Attachments = append(many.Attachments, domain.Attachment{Filename: "a.txt", Data: []byte("x")})
	}
	many.Source = &domain.PartSource{Folder: "INBOX", UID: 9, Parts: []string{"2"}}
	var verr *domain.ValidationError
	if _, err := h.svc.Send(ctx, sess, many, sendOpts(0)); !errors.As(err, &verr) || verr.Field != "attachments" {
		t.Fatalf("demasiados adjuntos entre subidos y del buzon: %v", err)
	}
	if h.mail.opened != opened {
		t.Fatal("el numero de adjuntos se comprueba antes de abrir el buzon")
	}

	d.Source.Parts = []string{"7"}
	if _, err := h.svc.Send(ctx, sess, d, sendOpts(0)); !errors.Is(err, domain.ErrPartNotFound) {
		t.Fatalf("parte inexistente: %v", err)
	}
	if len(h.sender.calls) != 0 {
		t.Fatal("nada salio")
	}
}

func TestSaveDraftConAdjuntosDelBorradorAnterior(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.parts = map[string]storedPart{"2": {part: domain.Part{ID: "2", Filename: "anexo.txt", ContentType: "text/plain"}, data: []byte("anexo")}}
	d := domain.Draft{Subject: "Borrador", Source: &domain.PartSource{Folder: "Drafts", UID: 5, Parts: []string{"2"}}}
	uid, err := h.svc.SaveDraft(ctx, sess, d, 5)
	if err != nil || uid != 7 {
		t.Fatalf("uid=%d err=%v", uid, err)
	}
	if got := h.composer.last.Attachments; len(got) != 1 || got[0].Filename != "anexo.txt" || string(got[0].Data) != "anexo" {
		t.Fatalf("el nuevo borrador conserva el adjunto del anterior: %+v", got)
	}
	if strings.Join(h.mb.expunged, ",") != "Drafts:5" {
		t.Fatalf("el borrador anterior se retira despues de leerlo: %v", h.mb.expunged)
	}
}
