package imap

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"github.com/alonsosss/corforce-email/services/webmail/internal/ports"
	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"go.uber.org/zap"
)

const (
	testMailbox = "ana@empresa.test"
	testMaster  = "webmail@platform.local"
	testSecret  = "maestra-de-prueba-de-treinta-y-dos"
)

// memMailbox abre un buzon contra un servidor IMAP en memoria sin THREAD ni SORT: el camino que se
// prueba es el de agrupar en el propio servicio.
func memMailbox(t *testing.T) ports.Mailbox {
	t.Helper()
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testMailbox+masterSeparator+testMaster, testSecret)
	for _, name := range []string{"INBOX", "Sent"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imaplib.CapSet{imaplib.CapIMAP4rev1: {}, imaplib.CapUIDPlus: {}, imaplib.CapMove: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	store, err := NewStore(Config{Addr: ln.Addr().String(), TLSMode: TLSNone, MasterUser: testMaster, MasterPassword: testSecret}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	mb, err := store.Open(context.Background(), testMailbox)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mb.Close() })
	return mb
}

func appendRaw(t *testing.T, mb ports.Mailbox, folder, raw string, flags ...domain.Flag) uint32 {
	t.Helper()
	ref, err := mb.Append(context.Background(), folder, []byte(raw), flags, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ref.UID
}

func rawMail(from, subject, date, extra string) string {
	return "From: " + from + "\r\nTo: ana@empresa.test\r\nSubject: " + subject + "\r\nDate: " + date + "\r\n" + extra + "\r\nCuerpo\r\n"
}

func TestLaPestanaDelListadoYElFiltroEnElServidorCoinciden(t *testing.T) {
	mb := memMailbox(t)
	ctx := context.Background()
	appendRaw(t, mb, "INBOX", rawMail("Cliente <c@cliente.test>", "Pedido", "Mon, 1 Sep 2026 10:00:00 +0000", ""))
	appendRaw(t, mb, "INBOX", rawMail("Tienda <news@tienda.test>", "Ofertas", "Mon, 1 Sep 2026 11:00:00 +0000",
		"List-Unsubscribe: <https://tienda.test/u>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n"))
	appendRaw(t, mb, "INBOX", rawMail("Sistema <sistema@proveedor.test>", "Alerta", "Mon, 1 Sep 2026 12:00:00 +0000",
		"Auto-Submitted: auto-generated\r\nList-Id: <alertas.proveedor.test>\r\n"))
	appendRaw(t, mb, "INBOX", rawMail("Banco <no-reply@banco.test>", "Movimiento", "Mon, 1 Sep 2026 13:00:00 +0000", ""))
	appendRaw(t, mb, "INBOX", rawMail("Colega <luis@empresa.test>", "Respuesta", "Mon, 1 Sep 2026 14:00:00 +0000", "Auto-Submitted: no\r\n"))

	q, _ := domain.NewListQuery(1, 50, "")
	all, err := mb.List(ctx, "INBOX", q)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.Category{
		"Pedido": domain.CategoryPrimary, "Ofertas": domain.CategoryNewsletters, "Alerta": domain.CategoryNotifications,
		"Movimiento": domain.CategoryNotifications, "Respuesta": domain.CategoryPrimary,
	}
	byCategory := map[domain.Category]map[uint32]bool{}
	for _, e := range all.Items {
		if e.Category != want[e.Subject] {
			t.Errorf("%s: categoria %q, quiero %q", e.Subject, e.Category, want[e.Subject])
		}
		if byCategory[e.Category] == nil {
			byCategory[e.Category] = map[uint32]bool{}
		}
		byCategory[e.Category][e.UID] = true
	}
	for _, c := range domain.Categories {
		filtered, err := q.WithFilter(domain.SearchFilter{Category: c})
		if err != nil {
			t.Fatal(err)
		}
		page, err := mb.List(ctx, "INBOX", filtered)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != len(byCategory[c]) {
			t.Errorf("%s: el servidor filtra %d, el listado clasifica %d", c, page.Total, len(byCategory[c]))
		}
		for _, e := range page.Items {
			if !byCategory[c][e.UID] {
				t.Errorf("%s: el servidor incluye %q, que el listado clasifica como %s", c, e.Subject, e.Category)
			}
		}
	}
}

func TestConversacionesSinTHREADAgrupanPorReferencias(t *testing.T) {
	mb := memMailbox(t)
	ctx := context.Background()
	a := appendRaw(t, mb, "INBOX", rawMail("Cliente <c@cliente.test>", "Presupuesto", "Mon, 1 Sep 2026 10:00:00 +0000", "Message-ID: <a@cliente.test>\r\n"), domain.FlagSeen)
	other := appendRaw(t, mb, "INBOX", rawMail("Otro <o@otro.test>", "Aparte", "Mon, 1 Sep 2026 11:00:00 +0000", "Message-ID: <x@otro.test>\r\n"))
	b := appendRaw(t, mb, "INBOX", rawMail("Cliente <c@cliente.test>", "Re: Presupuesto", "Mon, 1 Sep 2026 12:00:00 +0000",
		"Message-ID: <b@cliente.test>\r\nIn-Reply-To: <a@cliente.test>\r\n"))
	c := appendRaw(t, mb, "INBOX", rawMail("Socio <s@cliente.test>", "Re: Presupuesto", "Mon, 1 Sep 2026 13:00:00 +0000",
		"Message-ID: <c@cliente.test>\r\nReferences: <a@cliente.test> <b@cliente.test>\r\n"))
	sent := appendRaw(t, mb, "Sent", rawMail("Ana <ana@empresa.test>", "Re: Presupuesto", "Mon, 1 Sep 2026 12:30:00 +0000",
		"Message-ID: <r@empresa.test>\r\nIn-Reply-To: <b@cliente.test>\r\n"), domain.FlagSeen)

	q, _ := domain.NewListQuery(1, 10, "")
	page, err := mb.ListThreads(ctx, "INBOX", q)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Capped || len(page.Items) != 2 {
		t.Fatalf("pagina: %+v", page)
	}
	first := page.Items[0]
	if first.Latest.UID != c || first.Size != 3 || first.Unread != 2 || len(first.UIDs) != 3 || first.UIDs[2] != a {
		t.Fatalf("la conversacion con actividad mas reciente va primero: %+v", first)
	}
	if len(first.Participants) != 2 || first.Latest.Category != domain.CategoryPrimary {
		t.Fatalf("participantes: %+v", first.Participants)
	}
	if page.Items[1].Latest.UID != other || page.Items[1].Size != 1 {
		t.Fatalf("segunda: %+v", page.Items[1])
	}
	q2, _ := domain.NewListQuery(2, 1, "")
	second, err := mb.ListThreads(ctx, "INBOX", q2)
	if err != nil || second.Total != 2 || len(second.Items) != 1 || second.Items[0].Latest.UID != other {
		t.Fatalf("la paginacion es por conversaciones: %+v %v", second, err)
	}

	conv, err := mb.Conversation(ctx, "INBOX", b, domain.MaxThreadMessages)
	if err != nil {
		t.Fatal(err)
	}
	if len(conv) != 3 || conv[0].UID != c || conv[2].MessageID != "a@cliente.test" || conv[0].Folder != "INBOX" {
		t.Fatalf("conversacion: %+v", conv)
	}
	if _, err := mb.Conversation(ctx, "INBOX", 999, 10); err != domain.ErrMessageNotFound {
		t.Fatalf("un UID que no existe: %v", err)
	}
	related, err := mb.Related(ctx, "Sent", []string{"a@cliente.test", "b@cliente.test", "no valido con espacios"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != 1 || related[0].UID != sent || related[0].Folder != "Sent" {
		t.Fatalf("respuestas propias: %+v", related)
	}
	if none, err := mb.Related(ctx, "Sent", nil, 10); err != nil || none != nil {
		t.Fatalf("sin identificadores no se busca: %v %v", none, err)
	}
}

func TestInsightLeeLasCabecerasSinMarcarComoLeido(t *testing.T) {
	mb := memMailbox(t)
	ctx := context.Background()
	uid := appendRaw(t, mb, "INBOX", rawMail("\"Carlos Ruiz\" <carlos@gratis.test>", "Pago urgente", "Mon, 1 Sep 2026 10:00:00 +0000",
		"Reply-To: <cobros@otro.test>\r\nAuthentication-Results: mx.empresa.test;\r\n spf=fail smtp.mailfrom=gratis.test;\r\n dkim=none\r\n"+
			"X-Spamd-Result: default: False [3.00 / 15.00]; DMARC_POLICY_REJECT(2.00)[gratis.test : No valid SPF,reject]\r\n"))
	src, err := mb.Insight(ctx, "INBOX", uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.From) != 1 || src.From[0].Name != "Carlos Ruiz" || len(src.ReplyTo) != 1 || src.ReplyTo[0].Email != "cobros@otro.test" {
		t.Fatalf("direcciones: %+v", src)
	}
	auth := domain.ParseAuthentication(src.Headers)
	if auth.SPF != domain.AuthFail || auth.DMARC != domain.AuthFail || auth.DKIM != domain.AuthNone {
		t.Fatalf("autenticacion desplegada: %+v (%v)", auth, src.Headers)
	}
	q, _ := domain.NewListQuery(1, 10, "")
	page, _ := mb.List(ctx, "INBOX", q)
	if len(page.Items) != 1 || len(page.Items[0].Flags) != 0 {
		t.Fatalf("leer la ficha no marca el mensaje: %+v", page.Items)
	}
	if _, err := mb.Insight(ctx, "INBOX", 99); err != domain.ErrMessageNotFound {
		t.Fatalf("err=%v", err)
	}
}

func TestFlattenThreadRecorreLaArborescencia(t *testing.T) {
	tree := imapclient.ThreadData{Chain: []uint32{1, 2}, SubThreads: []imapclient.ThreadData{
		{Chain: []uint32{3}}, {Chain: []uint32{4}, SubThreads: []imapclient.ThreadData{{Chain: []uint32{5}}}},
	}}
	got := flattenThread(&tree)
	if len(got) != 5 || got[0] != 1 || got[4] != 5 {
		t.Fatalf("%v", got)
	}
}

func TestParseHeaderFieldsDespliegaLasLineas(t *testing.T) {
	h := parseHeaderFields([]byte("List-Id: Boletin\r\n <boletin.tienda.test>\r\nlist-unsubscribe: <https://t.test/u>\r\n\r\n"))
	if h.First(domain.HeaderListID) != "Boletin <boletin.tienda.test>" || !h.Has(domain.HeaderListUnsubscribe) {
		t.Fatalf("%v", h)
	}
	if len(parseHeaderFields(nil)) != 0 || len(parseHeaderFields([]byte("sin dos puntos\r\n"))) != 0 {
		t.Fatal("una cabecera ilegible no aporta nada")
	}
}

func TestCategoryCriteriaSinCategoriaNoFiltra(t *testing.T) {
	if categoryCriteria("") != nil {
		t.Fatal("sin pestana no hay criterio")
	}
	if c := categoryCriteria(domain.CategoryPrimary); c == nil || len(c.Not) != 3 {
		t.Fatalf("principal: %+v", c)
	}
}
