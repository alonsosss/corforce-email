package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

func convMsg(folder, id string, uid uint32, at time.Time) domain.ConversationMessage {
	return domain.ConversationMessage{Folder: folder, MessageID: id, Envelope: domain.Envelope{UID: uid, Date: at}}
}

func TestListThreadsValidaLaCarpetaYDelegaEnElBuzon(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.insight.threads = domain.ThreadPage{Total: 1, Items: []domain.ThreadSummary{{Latest: domain.Envelope{UID: 9}, UIDs: []uint32{9, 4}, Size: 2}}}
	q, _ := domain.NewListQuery(1, 10, "")
	page, err := h.svc.ListThreads(context.Background(), sess, "INBOX", q)
	if err != nil || page.Total != 1 || page.Items[0].Size != 2 || h.mb.listed != "INBOX" {
		t.Fatalf("pagina=%+v err=%v", page, err)
	}
	var verr *domain.ValidationError
	if _, err := h.svc.ListThreads(context.Background(), sess, "", q); !errors.As(err, &verr) {
		t.Fatalf("una carpeta vacia debe rechazarse: %v", err)
	}
}

func TestConversationSumaLasRespuestasPropiasDeEnviadosEnOrden(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	h.mb.insight.conversation = []domain.ConversationMessage{
		convMsg("INBOX", "c@x", 12, base.Add(3*time.Hour)),
		convMsg("INBOX", "a@x", 10, base),
	}
	h.mb.insight.related = []domain.ConversationMessage{
		convMsg("Sent", "b@empresa.pe", 3, base.Add(time.Hour)),
		convMsg("Sent", "b@empresa.pe", 3, base.Add(time.Hour)),
	}
	msgs, err := h.svc.Conversation(context.Background(), sess, "INBOX", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[0].UID != 10 || msgs[1].Folder != "Sent" || msgs[2].UID != 12 {
		t.Fatalf("la conversacion debe ir de la mas antigua a la mas reciente sin repetidos: %+v", msgs)
	}
	if h.mb.insight.relatedFolder != "Sent" || len(h.mb.insight.relatedIDs) != 2 {
		t.Fatalf("debe buscar en Enviados con los Message-ID: %q %v", h.mb.insight.relatedFolder, h.mb.insight.relatedIDs)
	}
}

func TestConversationEnEnviadosNoBuscaDosVecesYSobreviveAUnFalloDeEnviados(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	at := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	h.mb.insight.conversation = []domain.ConversationMessage{convMsg("Sent", "a@x", 1, at)}
	if _, err := h.svc.Conversation(context.Background(), sess, "Sent", 1); err != nil {
		t.Fatal(err)
	}
	if h.mb.insight.relatedFolder != "" {
		t.Fatalf("desde Enviados no se busca en Enviados: %q", h.mb.insight.relatedFolder)
	}
	h.mb.insight.conversation = []domain.ConversationMessage{convMsg("INBOX", "a@x", 1, at)}
	h.mb.insight.relatedErr = domain.ErrFolderNotFound
	msgs, err := h.svc.Conversation(context.Background(), sess, "INBOX", 1)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("un fallo en Enviados no debe impedir abrir la conversacion: %v %+v", err, msgs)
	}
	h.mb.insight.conversationErr = domain.ErrMessageNotFound
	if _, err := h.svc.Conversation(context.Background(), sess, "INBOX", 1); !errors.Is(err, domain.ErrMessageNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestOrderConversationAcotaALasMasRecientes(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var msgs []domain.ConversationMessage
	for i := 0; i < domain.MaxThreadMessages+5; i++ {
		msgs = append(msgs, domain.ConversationMessage{Folder: "INBOX", Envelope: domain.Envelope{UID: uint32(i + 1), Date: base.Add(time.Duration(i) * time.Minute)}})
	}
	out := orderConversation(msgs)
	if len(out) != domain.MaxThreadMessages || out[len(out)-1].UID != uint32(domain.MaxThreadMessages+5) || out[0].UID != 6 {
		t.Fatalf("len=%d primero=%d ultimo=%d", len(out), out[0].UID, out[len(out)-1].UID)
	}
}

// insightService arma el servicio del arnes con una libreta que responde por consulta.
func insightService(t *testing.T, h *harness, book *queryBook) *Service {
	t.Helper()
	d := h.deps()
	d.AddressBook = book
	svc, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestSenderInsightDetectaLaSuplantacionDeUnCompanero(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.insight.source = domain.InsightSource{
		From:    []domain.Address{{Name: "Carlos Ruiz", Email: "carlos.ruiz@gmail.com"}},
		Headers: domain.MessageHeaders{domain.HeaderAuthResults: {"rspamd; spf=pass smtp.mailfrom=gmail.com; dkim=pass; dmarc=pass"}},
	}
	book := &queryBook{byQuery: map[string][]domain.AddressBookEntry{
		"Carlos Ruiz": {{Address: "carlos@empresa.pe", DisplayName: "Carlos Ruiz"}},
	}}
	svc := insightService(t, h, book)
	got, err := svc.SenderInsight(context.Background(), sess, "INBOX", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Shield.Level != domain.ShieldDanger || !got.Shield.External || !hasReason(got.Shield, domain.ReasonColleagueName) {
		t.Fatalf("escudo: %+v", got.Shield)
	}
	if got.Sender == nil || got.Sender.Email != "carlos.ruiz@gmail.com" || got.Category != domain.CategoryPrimary {
		t.Fatalf("ficha: %+v", got)
	}
	if h.mb.insight.sourceFor != "INBOX" {
		t.Fatalf("carpeta: %q", h.mb.insight.sourceFor)
	}
}

func TestSenderInsightReconoceUnDominioDeLaEmpresaPorElDirectorio(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.insight.source = domain.InsightSource{From: []domain.Address{{Name: "Luis", Email: "luis@empresa-filial.pe"}}}
	book := &queryBook{byQuery: map[string][]domain.AddressBookEntry{
		"@empresa-filial.pe": {{Address: "luis@empresa-filial.pe", DisplayName: "Luis"}},
	}}
	svc := insightService(t, h, book)
	got, err := svc.SenderInsight(context.Background(), sess, "INBOX", 3)
	if err != nil {
		t.Fatal(err)
	}
	if got.Shield.External || got.Shield.Level != domain.ShieldNone {
		t.Fatalf("un dominio con buzones de la empresa es interno: %+v", got.Shield)
	}
}

func TestSenderInsightConDirectorioCaidoEsParcial(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.directory.err = errors.New("mail-directory caido")
	h.mb.insight.source = domain.InsightSource{
		From: []domain.Address{{Name: "Soporte", Email: "soporte@ernpresa.pe"}},
		Headers: domain.MessageHeaders{
			domain.HeaderListUnsubscribe:     {"<https://news.tienda.test/u/abc>"},
			domain.HeaderListUnsubscribePost: {"List-Unsubscribe=One-Click"},
		},
	}
	svc := insightService(t, h, &queryBook{err: errors.New("caido")})
	got, err := svc.SenderInsight(context.Background(), sess, "INBOX", 3)
	if err != nil {
		t.Fatalf("un directorio caido no debe impedir la ficha: %v", err)
	}
	if !got.Shield.Partial || !hasReason(got.Shield, domain.ReasonHomoglyphDomain) {
		t.Fatalf("aun sin directorio compara con el dominio del buzon: %+v", got.Shield)
	}
	if got.Unsubscribe.Method != domain.UnsubscribeOneClick || got.Category != domain.CategoryNewsletters {
		t.Fatalf("baja=%+v categoria=%s", got.Unsubscribe, got.Category)
	}
}

func TestSenderInsightPropagaElErrorDelBuzon(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.insight.sourceErr = domain.ErrMessageNotFound
	if _, err := h.svc.SenderInsight(context.Background(), sess, "INBOX", 3); !errors.Is(err, domain.ErrMessageNotFound) {
		t.Fatalf("err=%v", err)
	}
	if _, err := h.svc.SenderInsight(context.Background(), sess, "", 3); err == nil {
		t.Fatal("carpeta invalida")
	}
}

func TestUnsubscribeEnUnClicUsaLaURLDelMensaje(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.insight.source = domain.InsightSource{Headers: domain.MessageHeaders{
		domain.HeaderListUnsubscribe:     {"<mailto:baja@tienda.test>, <https://news.tienda.test/u/abc>"},
		domain.HeaderListUnsubscribePost: {"List-Unsubscribe=One-Click"},
	}}
	res, err := h.svc.Unsubscribe(context.Background(), sess, "INBOX", 3)
	if err != nil {
		t.Fatal(err)
	}
	if res.Method != domain.UnsubscribeOneClick || res.Target != "news.tienda.test" {
		t.Fatalf("resultado: %+v", res)
	}
	if len(h.unsub.targets) != 1 || h.unsub.targets[0] != "https://news.tienda.test/u/abc" || len(h.sender.calls) != 0 {
		t.Fatalf("baja: %v envios=%d", h.unsub.targets, len(h.sender.calls))
	}
	h.unsub.err = domain.ErrUnsubscribeRefused
	if _, err := h.svc.Unsubscribe(context.Background(), sess, "INBOX", 3); !errors.Is(err, domain.ErrUnsubscribeRefused) {
		t.Fatalf("err=%v", err)
	}
}

func TestUnsubscribePorCorreoSaleDelBuzonSinCopia(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.insight.source = domain.InsightSource{Headers: domain.MessageHeaders{
		domain.HeaderListUnsubscribe: {"<mailto:baja@tienda.test?subject=Baja%20123&cc=otro@x.test&body=quitar>"},
	}}
	res, err := h.svc.Unsubscribe(context.Background(), sess, "INBOX", 3)
	if err != nil {
		t.Fatal(err)
	}
	if res.Method != domain.UnsubscribeMailto || res.Target != "baja@tienda.test" {
		t.Fatalf("resultado: %+v", res)
	}
	if len(h.sender.calls) != 1 {
		t.Fatalf("envios: %d", len(h.sender.calls))
	}
	call := h.sender.calls[0]
	if call.username != testUser || call.from != testUser || len(call.rcpts) != 1 || call.rcpts[0] != "baja@tienda.test" {
		t.Fatalf("envio: %+v", call)
	}
	if h.composer.last.Subject != "Baja 123" || h.composer.last.Text != "quitar" {
		t.Fatalf("mensaje: %+v", h.composer.last)
	}
	if len(h.mb.appended) != 0 || len(h.unsub.targets) != 0 {
		t.Fatalf("el correo de baja no se guarda en Enviados: %d", len(h.mb.appended))
	}
}

func TestUnsubscribeSinBajaUtilizable(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.insight.source = domain.InsightSource{Headers: domain.MessageHeaders{
		domain.HeaderListUnsubscribe: {"<https://news.tienda.test/pagina>"},
	}}
	if _, err := h.svc.Unsubscribe(context.Background(), sess, "INBOX", 3); !errors.Is(err, domain.ErrUnsubscribeNotAvailable) {
		t.Fatalf("una pagina sin POST de un clic no la visita el servicio: %v", err)
	}
	h.mb.insight.source = domain.InsightSource{Headers: domain.MessageHeaders{
		domain.HeaderListUnsubscribe: {"<mailto:baja@tienda.test>"},
	}}
	h.sender.err = domain.ErrMessageRejected
	if _, err := h.svc.Unsubscribe(context.Background(), sess, "INBOX", 3); !errors.Is(err, domain.ErrMessageRejected) {
		t.Fatalf("err=%v", err)
	}
	if _, err := h.svc.Unsubscribe(context.Background(), sess, "", 3); err == nil {
		t.Fatal("carpeta invalida")
	}
}

func TestLaBajaNoSeOfreceNiSeHaceDesdeLoPropioNiDesdeSpam(t *testing.T) {
	h := newHarness(t)
	_, sess := h.login(t)
	h.mb.folders = append(h.mb.folders, domain.Folder{Name: "Junk", Role: domain.RoleJunk, Selectable: true})
	h.mb.insight.source = domain.InsightSource{Headers: domain.MessageHeaders{
		domain.HeaderListUnsubscribe:     {"<https://news.tienda.test/u/abc>"},
		domain.HeaderListUnsubscribePost: {"List-Unsubscribe=One-Click"},
	}}
	for _, folder := range []string{"Drafts", "Sent", "Junk"} {
		got, err := h.svc.SenderInsight(context.Background(), sess, folder, 3)
		if err != nil || got.Unsubscribe.Method != "" {
			t.Errorf("%s: la ficha no ofrece baja: %+v %v", folder, got.Unsubscribe, err)
		}
		if _, err := h.svc.Unsubscribe(context.Background(), sess, folder, 3); !errors.Is(err, domain.ErrUnsubscribeNotAvailable) {
			t.Errorf("%s: %v", folder, err)
		}
	}
	if len(h.unsub.targets) != 0 {
		t.Fatalf("no se contacto con nadie: %v", h.unsub.targets)
	}
	got, err := h.svc.SenderInsight(context.Background(), sess, "INBOX", 3)
	if err != nil || got.Unsubscribe.Method != domain.UnsubscribeOneClick {
		t.Fatalf("desde la bandeja si: %+v %v", got.Unsubscribe, err)
	}
}

func TestMetaSirveLasPestanasYElTopeDeConversacion(t *testing.T) {
	h := newHarness(t)
	m := h.svc.Meta()
	if m.MaxThreadMessages != domain.MaxThreadMessages || len(m.InboxCategories) != 3 || m.InboxCategories[0] != domain.CategoryPrimary {
		t.Fatalf("meta: %+v", m)
	}
}

func hasReason(s domain.Shield, code string) bool {
	for _, r := range s.Reasons {
		if r.Code == code {
			return true
		}
	}
	return false
}
