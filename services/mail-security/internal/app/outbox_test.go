package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

type reinyectorFalso struct {
	entregados int
	fallo      error
}

func (r *reinyectorFalso) Reinject(context.Context, string, string, []byte) error {
	if r.fallo != nil {
		return r.fallo
	}
	r.entregados++
	return nil
}

// /pipe guarda la fila y encola su evento en la misma transaccion; si la outbox falla no
// queda fila y Rspamd recibe el 503 de almacenamiento.
func TestPipeEncolaElEventoConLaFila(t *testing.T) {
	f := newEngineFixture()
	ctx := context.Background()
	tenant := uuid.New()
	f.dir.Mailboxes["ana@acme.com"] = domain.Mailbox{TenantID: tenant, Username: "ana@acme.com", Domain: "acme.com", Active: 1}
	meta := domain.QuarantineMetadata{QID: "Q1", Rcpt: []string{"ana@acme.com"}}

	if _, err := f.uc.Pipe(ctx, meta, []byte("m")); err != nil {
		t.Fatal(err)
	}
	if len(f.pub.Subjects) != 1 || len(f.pub.Outside) != 0 || len(f.q.Items) != 1 {
		t.Fatalf("fila y evento juntos: eventos=%v fuera=%v filas=%d", f.pub.Subjects, f.pub.Outside, len(f.q.Items))
	}

	f.pub.Fail = errors.New("outbox: permission denied")
	_, err := f.uc.Pipe(ctx, meta, []byte("m"))
	var storeErr *StoreError
	if !errors.As(err, &storeErr) {
		t.Fatalf("un fallo al encolar es un fallo de almacenamiento (503): %v", err)
	}
	if len(f.q.Items) != 1 || len(f.pub.Subjects) != 1 {
		t.Fatalf("la segunda fila debe deshacerse: filas=%d eventos=%v", len(f.q.Items), f.pub.Subjects)
	}
}

// La liberacion reinyecta, borra y encola en una transaccion: si algo falla la fila sigue
// y no sale evento; una segunda liberacion del mismo mensaje ya no lo encuentra.
func TestReleaseReinyectaBorraYEncolaEnUnaTransaccion(t *testing.T) {
	ctx := context.Background()
	tx := &apptest.Tx{}
	q := &apptest.Quarantine{}
	pub := &apptest.Publisher{Tx: tx}
	tx.Snapshot = func() func() {
		saved := append([]domain.QuarantineItem(nil), q.Items...)
		return func() { q.Items = saved }
	}
	reinj := &reinyectorFalso{}
	uc := NewQuarantineUseCase(QuarantineDeps{Tx: tx, Repo: q, Reinjector: reinj, Events: pub, Logger: zap.NewNop()})
	tenant := uuid.New()
	item := domain.QuarantineItem{ID: uuid.New(), TenantID: tenant, Rcpt: "ana@acme.com", Sender: "x@y.com", Score: decimal.NewFromInt(12), Msg: []byte("m")}
	q.Items = []domain.QuarantineItem{item}

	reinj.fallo = errors.New("postfix no responde")
	if err := uc.Release(ctx, tenant, item.ID, "u1"); err == nil {
		t.Fatal("sin reinyeccion no hay liberacion")
	}
	if len(q.Items) != 1 || len(pub.Subjects) != 0 {
		t.Fatalf("reinyeccion fallida: filas=%d eventos=%v", len(q.Items), pub.Subjects)
	}

	reinj.fallo = nil
	pub.Fail = errors.New("outbox caida")
	if err := uc.Release(ctx, tenant, item.ID, "u1"); err == nil {
		t.Fatal("sin evento no se confirma la liberacion")
	}
	if len(q.Items) != 1 || reinj.entregados != 1 {
		t.Fatalf("la fila sigue aunque el mensaje ya se entrego: filas=%d entregas=%d", len(q.Items), reinj.entregados)
	}

	pub.Fail = nil
	if err := uc.Release(ctx, tenant, item.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	if len(q.Items) != 0 || len(pub.Subjects) != 1 || pub.Subjects[0] != domain.SubjectQuarantineReleased || len(pub.Outside) != 0 {
		t.Fatalf("liberacion: filas=%d eventos=%v fuera=%v", len(q.Items), pub.Subjects, pub.Outside)
	}
	if err := uc.Release(ctx, tenant, item.ID, "u1"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("segunda liberacion: %v", err)
	}
}

// La marca de /settings vive en la base: una replica recien arrancada responde igual que
// la otra, y un cambio visto por una se sirve con la misma marca desde las dos.
func TestSettingsMarcaCompartidaEntreReplicas(t *testing.T) {
	f := newEngineFixture()
	ctx := context.Background()
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	a := f.uc
	a.now = func() time.Time { return base }

	doc, _, err := a.Settings(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	b := f.replica()
	b.now = func() time.Time { return base.Add(time.Hour) }
	doc2, notModified, err := b.Settings(ctx, doc.LastModified)
	if err != nil || !notModified || !doc2.LastModified.Equal(doc.LastModified) {
		t.Fatalf("replica nueva: %v 304=%v %v vs %v", err, notModified, doc2.LastModified, doc.LastModified)
	}

	f.policy.Scores = []domain.SpamScore{{Object: "acme.com", HighScore: decimal.NewFromInt(15), LowScore: decimal.NewFromInt(8)}}
	doc3, notModified, _ := b.Settings(ctx, doc.LastModified)
	if notModified || !doc3.LastModified.After(doc.LastModified) {
		t.Fatalf("B ve el cambio: 304=%v %v", notModified, doc3.LastModified)
	}
	doc4, notModified, _ := a.Settings(ctx, doc.LastModified)
	if notModified || !doc4.LastModified.Equal(doc3.LastModified) || !strings.Contains(doc4.Body, "acme") {
		t.Fatalf("A sirve la marca que guardo B: 304=%v %v vs %v", notModified, doc4.LastModified, doc3.LastModified)
	}
	if f.docs.Saves != 2 {
		t.Fatalf("alta y un cambio: %d escrituras", f.docs.Saves)
	}
}

func TestSMTPAccessEnRedisYReconciliacion(t *testing.T) {
	f := newEngineFixture()
	ctx := context.Background()
	sync := NewRedisSync(f.store, f.dir, f.policy, zap.NewNop())
	netsOf := func(user string) map[string]string { return f.store.Hashes[domain.SMTPAllowNetsKey(user)] }

	ana := domain.SMTPAccess{Username: "ana@acme.com", Networks: []string{"203.0.113.0/24", "198.51.100.7"}}
	if err := sync.SyncSMTPAccess(ctx, ana); err != nil {
		t.Fatal(err)
	}
	if f.store.Hashes[domain.RedisSMTPLimitedAccess]["ana@acme.com"] != "1" || len(netsOf("ana@acme.com")) != 2 || netsOf("ana@acme.com")["198.51.100.7"] != "1" {
		t.Fatalf("alta: %v", f.store.Hashes)
	}

	// Restos: un usuario que ya no esta limitado y una red retirada.
	_ = f.store.HSet(ctx, domain.SMTPAllowNetsKey("viejo@acme.com"), "10.0.0.0/8", "1")
	_ = f.store.HSet(ctx, domain.RedisSMTPLimitedAccess, "viejo@acme.com", "1")
	f.policy.SMTP = []domain.SMTPAccess{{Username: "ana@acme.com", Networks: []string{"203.0.113.0/24"}}}
	if err := sync.ReconcileAll(ctx); err != nil {
		t.Fatal(err)
	}
	limited := f.store.Hashes[domain.RedisSMTPLimitedAccess]
	if len(limited) != 1 || limited["ana@acme.com"] != "1" {
		t.Fatalf("SMTP_LIMITED_ACCESS: %v", limited)
	}
	if _, ok := f.store.Hashes[domain.SMTPAllowNetsKey("viejo@acme.com")]; ok {
		t.Fatal("la clave de un usuario sin restriccion debe desaparecer")
	}
	if nets := netsOf("ana@acme.com"); len(nets) != 1 || nets["203.0.113.0/24"] != "1" {
		t.Fatalf("redes de ana: %v", nets)
	}

	if err := sync.RemoveSMTPAccess(ctx, "ana@acme.com"); err != nil {
		t.Fatal(err)
	}
	if len(f.store.Hashes[domain.RedisSMTPLimitedAccess]) != 0 || netsOf("ana@acme.com") != nil {
		t.Fatalf("baja: %v", f.store.Hashes)
	}
}

func TestFirewallSoloPlataformaListasOpcionesYDesbaneo(t *testing.T) {
	f := newEngineFixture()
	ctx := context.Background()
	uc := NewFirewallUseCase(FirewallDeps{Tx: f.tx, Repo: &apptest.Firewall{Reader: f.policy}, Policy: f.policy,
		Sync: NewRedisSync(f.store, f.dir, f.policy, zap.NewNop()), Store: f.store, Logger: zap.NewNop()})

	if _, err := uc.AddNetwork(ctx, false, domain.FirewallDeny, "203.0.113.9", ""); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("una empresa no toca el cortafuegos de la celda: %v", err)
	}
	if _, err := uc.Bans(ctx, false); !errors.Is(err, domain.ErrPlatformOnly) {
		t.Fatalf("ni lo lee: %v", err)
	}
	n, err := uc.AddNetwork(ctx, true, domain.FirewallDeny, "203.0.113.9", "abuso")
	if err != nil || n.Network != "203.0.113.9/32" || f.store.Hashes[domain.RedisF2BBlacklist]["203.0.113.9/32"] != "1" {
		t.Fatalf("red denegada: %+v %v %v", n, err, f.store.Hashes[domain.RedisF2BBlacklist])
	}
	if _, err := uc.AddNetwork(ctx, true, domain.FirewallAllow, "203.0.113.9/32", ""); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("una red solo esta en una lista: %v", err)
	}
	if err := uc.DeleteNetwork(ctx, true, n.ID); err != nil || len(f.store.Hashes[domain.RedisF2BBlacklist]) != 0 {
		t.Fatalf("baja: %v %v", err, f.store.Hashes[domain.RedisF2BBlacklist])
	}

	// Las opciones se mezclan con las claves que mantiene netfilter.
	f.store.Values[domain.RedisF2BOptions] = `{"ban_time":1800,"banlist_id":"abc","manage_external":0}`
	o := domain.DefaultFirewallOptions()
	o.BanTime, o.MaxBanTime = 3600, 86400
	if _, err := uc.PutOptions(ctx, true, o); err != nil {
		t.Fatal(err)
	}
	stored := f.store.Values[domain.RedisF2BOptions]
	if !strings.Contains(stored, `"ban_time":3600`) || !strings.Contains(stored, `"banlist_id":"abc"`) || !strings.Contains(stored, `"manage_external":0`) {
		t.Fatalf("F2B_OPTIONS: %s", stored)
	}
	bad := o
	bad.NetbanIPv6 = 200
	if _, err := uc.PutOptions(ctx, true, bad); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("opciones invalidas: %v", err)
	}
	// Redis vaciado: la reconciliacion reconstruye listas y opciones desde la base.
	if _, err := uc.AddNetwork(ctx, true, domain.FirewallAllow, "198.51.100.0/24", "oficina"); err != nil {
		t.Fatal(err)
	}
	f.store.Hashes, f.store.Values = map[string]map[string]string{}, map[string]string{}
	if err := NewRedisSync(f.store, f.dir, f.policy, zap.NewNop()).ReconcileAll(ctx); err != nil {
		t.Fatal(err)
	}
	if f.store.Hashes[domain.RedisF2BWhitelist]["198.51.100.0/24"] != "1" || !strings.Contains(f.store.Values[domain.RedisF2BOptions], `"max_ban_time":86400`) {
		t.Fatalf("reconciliacion: %v %v", f.store.Hashes, f.store.Values)
	}

	_ = f.store.HSet(ctx, domain.RedisF2BActiveBans, "192.0.2.0/24", "1893456000")
	_ = f.store.HSet(ctx, domain.RedisF2BPermBans, "10.9.0.0/16", "1700000000")
	bans, err := uc.Bans(ctx, true)
	if err != nil || len(bans) != 2 {
		t.Fatalf("baneos: %+v %v", bans, err)
	}
	if err := uc.Unban(ctx, true, "192.0.2.0/24"); err != nil || f.store.Hashes[domain.RedisF2BQueueUnban]["192.0.2.0/24"] != "1" {
		t.Fatalf("desbaneo: %v %v", err, f.store.Hashes[domain.RedisF2BQueueUnban])
	}
	if err := uc.Unban(ctx, true, "203.0.113.0/24"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("una red sin baneo vigente: %v", err)
	}
}
