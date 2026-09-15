package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

type engineFixture struct {
	uc     *EngineUseCase
	tx     *apptest.Tx
	docs   *apptest.Documents
	dir    *apptest.Directory
	policy *apptest.PolicyReader
	store  *apptest.Store
	q      *apptest.Quarantine
	pub    *apptest.Publisher
}

func newEngineFixture() *engineFixture {
	f := &engineFixture{tx: &apptest.Tx{}, docs: apptest.NewDocuments(), dir: apptest.NewDirectory(),
		policy: apptest.NewPolicyReader(), store: apptest.NewStore(), q: &apptest.Quarantine{}}
	f.pub = &apptest.Publisher{Tx: f.tx}
	// Un fallo dentro de la transaccion deshace lo guardado en la cuarentena en memoria.
	f.tx.Snapshot = func() func() {
		saved := append([]domain.QuarantineItem(nil), f.q.Items...)
		return func() { f.q.Items = saved }
	}
	f.uc = f.replica()
	return f
}

// replica es otra instancia del servicio sobre la misma base (y el mismo Redis).
func (f *engineFixture) replica() *EngineUseCase {
	logger := zap.NewNop()
	return NewEngineUseCase(EngineDeps{
		Tx: f.tx, Documents: f.docs, Directory: f.dir, Policy: f.policy, Quarantine: f.q,
		Sync:  NewRedisSync(f.store, f.dir, f.policy, logger),
		Store: f.store, Events: f.pub, Logger: logger, LogLines: 3,
	})
}

func TestSettingsRespondeNotModifiedYAvanzaAlCambiar(t *testing.T) {
	f := newEngineFixture()
	base := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	f.uc.now = func() time.Time { return base }
	f.policy.UpdatedAt = base.Add(-time.Hour)

	doc, notModified, err := f.uc.Settings(context.Background(), time.Time{})
	if err != nil || notModified {
		t.Fatalf("primera peticion: %v, notModified=%v", err, notModified)
	}
	if !doc.LastModified.Equal(f.policy.UpdatedAt) {
		t.Fatalf("tras arrancar, Last-Modified debe ser el ultimo updated_at: %v", doc.LastModified)
	}
	if !strings.Contains(doc.Body, "watchdog {") {
		t.Fatalf("falta el watchdog:\n%s", doc.Body)
	}

	_, notModified, _ = f.uc.Settings(context.Background(), doc.LastModified)
	if !notModified {
		t.Fatal("sin cambios debe responder 304")
	}

	// Un umbral nuevo cambia el contenido: la marca avanza y ya no es 304.
	f.policy.Scores = []domain.SpamScore{{Object: "acme.com", HighScore: decimal.NewFromInt(15), LowScore: decimal.NewFromInt(8)}}
	doc2, notModified, _ := f.uc.Settings(context.Background(), doc.LastModified)
	if notModified || !doc2.LastModified.After(doc.LastModified) {
		t.Fatalf("tras un cambio no debe ser 304 (%v) y la marca debe avanzar (%v > %v)", notModified, doc2.LastModified, doc.LastModified)
	}
	if !strings.Contains(doc2.Body, `"/@acme[.]com$/i"`) {
		t.Fatalf("el umbral no aparece:\n%s", doc2.Body)
	}

	// Un borrado tambien cambia el contenido aunque updated_at no se mueva.
	f.policy.Scores = nil
	doc3, notModified, _ := f.uc.Settings(context.Background(), doc2.LastModified)
	if notModified || !doc3.LastModified.After(doc2.LastModified) {
		t.Fatalf("un borrado debe invalidar el 304: %v %v", notModified, doc3.LastModified)
	}
}

func TestPipeGuardaPorBuzonFinalConLimitesDeSuEmpresa(t *testing.T) {
	f := newEngineFixture()
	tenantA, tenantB := uuid.New(), uuid.New()
	f.dir.Mailboxes["ana@acme.com"] = domain.Mailbox{TenantID: tenantA, Username: "ana@acme.com", Domain: "acme.com", Active: 1}
	f.dir.Mailboxes["luis@otra.com"] = domain.Mailbox{TenantID: tenantB, Username: "luis@otra.com", Domain: "otra.com", Active: 1}
	f.dir.Mailboxes["ex@excluida.com"] = domain.Mailbox{TenantID: tenantB, Username: "ex@excluida.com", Domain: "excluida.com", Active: 1}
	f.dir.Aliases["todos@acme.com"] = "ana@acme.com,luis@otra.com,ex@excluida.com"
	small := domain.DefaultQuarantineSettings(tenantB)
	small.MaxSizeBytes = 5
	small.ExcludeDomains = []string{"excluida.com"}
	f.policy.QSettings[tenantB] = small

	meta := domain.QuarantineMetadata{QID: "Q1", Subject: "hola", Score: decimal.RequireFromString("12.5"), Rcpt: []string{"todos@acme.com", "nadie@ajeno.com"}, From: "spam@x.com"}
	out, err := f.uc.Pipe(context.Background(), meta, []byte("mensaje de mas de cinco bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Stored != 1 || out.SkippedSize != 1 || out.SkippedDomain != 1 || out.NoMailbox != 1 {
		t.Fatalf("resultado inesperado: %+v", out)
	}
	if len(f.q.Items) != 1 || f.q.Items[0].TenantID != tenantA || f.q.Items[0].Rcpt != "ana@acme.com" || f.q.Items[0].QHash == "" {
		t.Fatalf("fila guardada incorrecta: %+v", f.q.Items)
	}
	if len(f.pub.Subjects) != 1 || f.pub.Subjects[0] != domain.SubjectQuarantineStored {
		t.Fatalf("evento no publicado: %v", f.pub.Subjects)
	}
}

func TestPipePodaPorRetencion(t *testing.T) {
	f := newEngineFixture()
	tenant := uuid.New()
	f.dir.Mailboxes["ana@acme.com"] = domain.Mailbox{TenantID: tenant, Username: "ana@acme.com", Domain: "acme.com", Active: 1}
	s := domain.DefaultQuarantineSettings(tenant)
	s.RetentionSize = 2
	f.policy.QSettings[tenant] = s
	for i := 0; i < 4; i++ {
		if _, err := f.uc.Pipe(context.Background(), domain.QuarantineMetadata{QID: "Q", Rcpt: []string{"ana@acme.com"}}, []byte("m")); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.q.Items) != 2 {
		t.Fatalf("deberian quedar 2 filas, hay %d", len(f.q.Items))
	}
}

func TestPipeRateLimitApilaYRecorta(t *testing.T) {
	f := newEngineFixture()
	for i := 0; i < 5; i++ {
		err := f.uc.PipeRateLimit(context.Background(), domain.RateLimitLog{RLInfo: "user(abc)", Rcpt: []string{"a@b.com"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	got := f.store.Lists[domain.RedisRateLimitLog]
	if len(got) != 3 || !strings.Contains(got[0], `"rl_name":"user"`) || !strings.Contains(got[0], `"rl_hash":"abc"`) {
		t.Fatalf("RL_LOG inesperado: %v", got)
	}
	f.store.Down = true
	if err := f.uc.PipeRateLimit(context.Background(), domain.RateLimitLog{}); err != domain.ErrRedisUnavailable {
		t.Fatalf("sin redis debe distinguirse: %v", err)
	}
}

func TestFooterExcluyeBuzonesYDominiosAlias(t *testing.T) {
	f := newEngineFixture()
	f.policy.Footers["acme.com"] = domain.DomainFooter{Domain: "acme.com", HTML: "<p>pie</p>", Plain: "pie", MailboxExclude: []string{"jefe@acme.com"}, AliasDomainExclude: []string{"acme-alias.com"}, SkipReplies: true}

	resp, _ := f.uc.Footer(context.Background(), "acme.com", "ana@acme.com", "ana@acme.com")
	if resp.HTML != "<p>pie</p>" || resp.SkipReplies != 1 || !strings.Contains(resp.Vars, `"from":"ana@acme.com"`) {
		t.Fatalf("pie esperado, obtuve %+v", resp)
	}
	if resp, _ := f.uc.Footer(context.Background(), "acme.com", "jefe@acme.com", "jefe@acme.com"); resp.HTML != "" {
		t.Fatalf("buzon excluido no lleva pie: %+v", resp)
	}
	if resp, _ := f.uc.Footer(context.Background(), "acme.com", "ana@acme.com", "ana@acme-alias.com"); resp.HTML != "" {
		t.Fatalf("dominio alias excluido no lleva pie: %+v", resp)
	}
	if resp, _ := f.uc.Footer(context.Background(), "otro.com", "x@otro.com", "x@otro.com"); resp.Vars != "{}" || resp.HTML != "" {
		t.Fatalf("sin pie debe ir vacio: %+v", resp)
	}
}

func TestReconcileAllDejaRedisComoLaBase(t *testing.T) {
	f := newEngineFixture()
	tenant := uuid.New()
	f.dir.Domains["acme.com"] = tenant
	f.dir.AliasDomains["acme-alias.com"] = "acme.com"
	f.policy.RL = []domain.RateLimit{{Object: "acme.com", Value: "100 / 1h"}}
	f.policy.FwdHosts = []domain.ForwardingHost{{Host: "10.0.0.0/24", Source: "relay", FilterSpam: false}}
	f.policy.Tags = []domain.MailboxTags{{Username: "ana@acme.com", SubjectTag: true}}
	s := domain.DefaultQuarantineSettings(tenant)
	s.MaxSizeBytes = 30 * 1024 * 1024
	s.ExcludeDomains = []string{"z.com"}
	f.policy.QSettings[tenant] = s
	// Restos de un estado anterior que deben desaparecer.
	_ = f.store.HSet(context.Background(), domain.RedisDomainMap, "viejo.com", "1")
	_ = f.store.HSet(context.Background(), domain.RedisRateLimitValue, "viejo@acme.com", "1 / 1h")

	sync := NewRedisSync(f.store, f.dir, f.policy, zap.NewNop())
	if err := sync.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	dm := f.store.Hashes[domain.RedisDomainMap]
	if len(dm) != 2 || dm["acme.com"] != "1" || dm["acme-alias.com"] != "1" {
		t.Fatalf("DOMAIN_MAP = %v", dm)
	}
	if rl := f.store.Hashes[domain.RedisRateLimitValue]; len(rl) != 1 || rl["acme.com"] != "100 / 1h" {
		t.Fatalf("RL_VALUE = %v", rl)
	}
	if f.store.Hashes[domain.RedisWhitelistedFwdHost]["10.0.0.0/24"] != "relay" || f.store.Hashes[domain.RedisKeepSpam]["10.0.0.0/24"] != "1" {
		t.Fatalf("hosts de reenvio: %v / %v", f.store.Hashes[domain.RedisWhitelistedFwdHost], f.store.Hashes[domain.RedisKeepSpam])
	}
	if f.store.Hashes[domain.RedisWantsSubjectTag]["ana@acme.com"] != "1" || len(f.store.Hashes[domain.RedisWantsSubfolderTag]) != 0 {
		t.Fatalf("etiquetas: %v", f.store.Hashes)
	}
	if f.store.Values[domain.RedisQuarantineMaxSize] != "30" || f.store.Values[domain.RedisQuarantineMaxAge] != "365" ||
		f.store.Values[domain.RedisQuarantineRetain] != "100" || f.store.Values[domain.RedisQuarantineExclude] != `["z.com"]` {
		t.Fatalf("Q_*: %v", f.store.Values)
	}
}

func TestRefreshDomainArrastraASusDominiosAlias(t *testing.T) {
	f := newEngineFixture()
	ctx := context.Background()
	sync := NewRedisSync(f.store, f.dir, f.policy, zap.NewNop())
	f.dir.Domains["acme.com"] = uuid.New()
	f.dir.AliasDomains["acme-alias.com"] = "acme.com"
	if err := sync.RefreshDomain(ctx, "acme.com"); err != nil {
		t.Fatal(err)
	}
	if dm := f.store.Hashes[domain.RedisDomainMap]; dm["acme.com"] != "1" || dm["acme-alias.com"] != "1" {
		t.Fatalf("alta: %v", dm)
	}
	// Se desactiva el dominio: su dominio alias sigue en el directorio pero se queda sin
	// destino, y un solo evento (el del dominio) debe retirar ambos.
	delete(f.dir.Domains, "acme.com")
	if err := sync.RefreshDomain(ctx, "acme.com"); err != nil {
		t.Fatal(err)
	}
	if dm := f.store.Hashes[domain.RedisDomainMap]; len(dm) != 0 {
		t.Fatalf("baja en cascada: %v", dm)
	}
}

func TestDKIMRotacionConvivenDosSelectores(t *testing.T) {
	f := newEngineFixture()
	ctx := context.Background()
	sync := NewRedisSync(f.store, f.dir, f.policy, zap.NewNop())
	pem := "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----"
	if err := sync.SyncDKIM(ctx, domain.DKIMKey{Domain: "acme.com", Selector: "s1", PrivateKeyPEM: pem}); err != nil {
		t.Fatal(err)
	}
	if err := sync.SyncDKIM(ctx, domain.DKIMKey{Domain: "acme.com", Selector: "s2", PrivateKeyPEM: pem}); err != nil {
		t.Fatal(err)
	}
	// Otro dominio con sufijo parecido no debe verse afectado por las bajas de acme.com.
	if err := sync.SyncDKIM(ctx, domain.DKIMKey{Domain: "sub.acme.com", Selector: "s1", PrivateKeyPEM: pem}); err != nil {
		t.Fatal(err)
	}
	keys := f.store.Hashes[domain.RedisDKIMPrivKeys]
	if keys["s1.acme.com"] != pem || keys["s2.acme.com"] != pem || f.store.Hashes[domain.RedisDKIMSelectors]["acme.com"] != "s2" {
		t.Fatalf("rotacion: %v / %v", keys, f.store.Hashes[domain.RedisDKIMSelectors])
	}

	// Retirar el selector viejo no toca el activo.
	if err := sync.RemoveDKIMSelector(ctx, "acme.com", "s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["s1.acme.com"]; ok || f.store.Hashes[domain.RedisDKIMSelectors]["acme.com"] != "s2" {
		t.Fatalf("tras retirar s1: %v / %v", keys, f.store.Hashes[domain.RedisDKIMSelectors])
	}
	// Retirar el activo deja el dominio sin selector.
	if err := sync.RemoveDKIMSelector(ctx, "acme.com", "s2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.store.Hashes[domain.RedisDKIMSelectors]["acme.com"]; ok {
		t.Fatalf("el selector activo debia retirarse: %v", f.store.Hashes[domain.RedisDKIMSelectors])
	}

	// Baja del dominio completo: solo sus claves, no las de sub.acme.com.
	_ = sync.SyncDKIM(ctx, domain.DKIMKey{Domain: "acme.com", Selector: "s3", PrivateKeyPEM: pem})
	if n, err := sync.RemoveDKIMDomain(ctx, "acme.com"); err != nil || n != 1 {
		t.Fatalf("baja de dominio: %d claves, %v", n, err)
	}
	if _, ok := keys["s3.acme.com"]; ok || keys["s1.sub.acme.com"] != pem || f.store.Hashes[domain.RedisDKIMSelectors]["sub.acme.com"] != "s1" {
		t.Fatalf("baja de dominio: %v / %v", keys, f.store.Hashes[domain.RedisDKIMSelectors])
	}
}
