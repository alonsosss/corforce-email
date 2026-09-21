//go:build integration

package postgres

// Prueba de integracion contra un Postgres real. Se ejecuta con:
//
//	DOMAIN_SERVICE_TEST_DSN=postgres://user:pass@localhost:5432/db?sslmode=disable \
//	  go test -tags integration ./services/domain-service/internal/adapters/postgres/
//
// Aplica las migraciones canonicas del servicio en su orden, dos veces (idempotencia), y
// ejercita todas las consultas del repositorio sobre una base desechable.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/pkg/db"
	outboxadapter "github.com/alonsosss/corforce-email/services/domain-service/internal/adapters/outbox"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationEnv devuelve la variable de entorno que apunta a la infraestructura de la
// prueba. Sin ella la prueba se salta, salvo con INTEGRATION_REQUIRED=1 (make
// test-integration y CI): ahi es un fallo, porque un salto esconderia que no llego.
func integrationEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("INTEGRATION_REQUIRED") == "1" {
			t.Fatalf("%s no definida con INTEGRATION_REQUIRED=1", name)
		}
		t.Skipf("%s no definida", name)
	}
	return v
}

func setup(t *testing.T) (context.Context, *Repository) {
	t.Helper()
	dsn := integrationEnv(t, "DOMAIN_SERVICE_TEST_DSN")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	t.Cleanup(pool.Close)

	canonical := filepath.Join("..", "..", "..", "..", "..", "migrations", "tenant", "canonical")
	files, err := filepath.Glob(filepath.Join(canonical, "domain-service", "*.sql"))
	if err != nil || len(files) < 3 {
		t.Fatalf("migraciones del servicio: %v %v", files, err)
	}
	sort.Strings(files)
	// La outbox de la base de empresa: los eventos de claves se encolan en la misma transaccion.
	files = append([]string{filepath.Join(canonical, "platform", "00_outbox.sql")}, files...)
	for i := 0; i < 2; i++ {
		for _, f := range files {
			sqlBytes, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("leer migracion %s: %v", f, err)
			}
			if _, err := pool.Exec(ctx, string(sqlBytes)); err != nil {
				t.Fatalf("aplicar migracion %s (pasada %d): %v", filepath.Base(f), i+1, err)
			}
		}
	}
	return db.WithPool(ctx, pool), NewRepository(&db.ContextPool{})
}

// La marca de desactivacion pendiente nace apagada, se guarda con Update y solo la ve el
// barrido de su empresa.
func TestRepositoryDeactivationPending(t *testing.T) {
	ctx, repo := setup(t)
	tenantID := uuid.New()
	d := sample(tenantID, "caido-"+uuid.NewString()[:8]+".test")
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	other := sample(tenantID, "sano-"+uuid.NewString()[:8]+".test")
	if err := repo.Create(ctx, other); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetByID(ctx, tenantID, d.ID)
	if err != nil || got.DirectoryDeactivationPending {
		t.Fatalf("nace con la marca: %v %v", got, err)
	}
	if pending, err := repo.ListPendingDeactivation(ctx, tenantID); err != nil || len(pending) != 0 {
		t.Fatalf("sin marcas: %d %v", len(pending), err)
	}

	got.Status, got.DirectoryDeactivationPending = domain.StatusFailed, true
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	pending, err := repo.ListPendingDeactivation(ctx, tenantID)
	if err != nil || len(pending) != 1 || pending[0].ID != d.ID || !pending[0].DirectoryDeactivationPending || pending[0].Status != domain.StatusFailed {
		t.Fatalf("pendientes: %+v %v", pending, err)
	}
	if pending, _ := repo.ListPendingDeactivation(ctx, uuid.New()); len(pending) != 0 {
		t.Error("otra empresa no ve las marcas de esta")
	}

	got.DirectoryDeactivationPending = false
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if pending, _ := repo.ListPendingDeactivation(ctx, tenantID); len(pending) != 0 {
		t.Errorf("la marca quitada sigue: %d", len(pending))
	}
}

func sample(tenantID uuid.UUID, name string) *domain.Domain {
	return &domain.Domain{
		ID: uuid.New(), TenantID: tenantID, Domain: name,
		Purpose: domain.PurposeCorporate, Status: domain.StatusPending,
		VerificationToken: "0123456789abcdef0123456789abcdef",
		DKIMSelector:      "cfm202609", DKIMPrivateKeyEnc: []byte{1, 2, 3}, DKIMPublicKey: "PUB",
		DKIMKeyBits: 2048, DMARCPolicy: domain.DMARCQuarantine,
	}
}

func TestRepositoryRoundTrip(t *testing.T) {
	ctx, repo := setup(t)
	tenantID := uuid.New()
	d := sample(tenantID, "acme-"+uuid.NewString()[:8]+".test")

	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if d.CreatedAt.IsZero() {
		t.Error("Create debe devolver los sellos de tiempo")
	}
	if err := repo.Create(ctx, sample(tenantID, d.Domain)); !errors.Is(err, domain.ErrDomainAlreadyExists) {
		t.Errorf("duplicado: %v", err)
	}
	if err := repo.Create(ctx, sample(uuid.New(), d.Domain)); err != nil {
		t.Errorf("el mismo nombre en otra empresa es valido: %v", err)
	}

	got, err := repo.GetByName(ctx, tenantID, d.Domain)
	if err != nil || got.ID != d.ID {
		t.Fatalf("GetByName: %v", err)
	}
	if _, err := repo.GetByID(ctx, uuid.New(), d.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("otra empresa no ve la fila: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	got.Status = domain.StatusVerified
	got.VerifiedAt, got.LastCheckedAt = &now, &now
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got.DKIMPreviousSelector, got.DKIMPreviousPrivateKeyEnc, got.DKIMPreviousPublicKey, got.DKIMRotatedAt = "cfm202608", []byte{9}, "OLD", &now
	if err := repo.SaveDKIMKeys(ctx, got, "cfm202609", &domain.DKIMRotation{
		TenantID: tenantID, DomainID: d.ID, Kind: domain.RotationScheduled, Selector: "cfm202609", PreviousSelector: "cfm202608", RotatedAt: now,
	}); err != nil {
		t.Fatalf("SaveDKIMKeys: %v", err)
	}
	again, _ := repo.GetByID(ctx, tenantID, d.ID)
	if again.Status != domain.StatusVerified || !again.HasPreviousDKIM() || again.DKIMPreviousPublicKey != "OLD" || !again.UpdatedAt.After(again.CreatedAt) {
		t.Errorf("Update no persistio: %+v", again)
	}

	checks := []domain.DNSCheck{
		{TenantID: tenantID, DomainID: d.ID, CheckedAt: now.Add(-time.Hour), Record: domain.RecordSPF, Expected: "a", Observed: "", OK: false, Detail: "viejo"},
		{TenantID: tenantID, DomainID: d.ID, CheckedAt: now, Record: domain.RecordSPF, Expected: "a", Observed: "a", OK: true},
		{TenantID: tenantID, DomainID: d.ID, CheckedAt: now, Record: domain.RecordDKIMPrevious, Expected: "b", OK: true},
	}
	if err := repo.SaveChecks(ctx, checks); err != nil {
		t.Fatalf("SaveChecks: %v", err)
	}
	latest, err := repo.LatestChecks(ctx, tenantID, d.ID)
	if err != nil || len(latest) != 2 {
		t.Fatalf("LatestChecks = %d, %v", len(latest), err)
	}
	for _, c := range latest {
		if c.Record == domain.RecordSPF && !c.OK {
			t.Error("LatestChecks debe devolver la comprobacion mas reciente de cada registro")
		}
	}

	recheck, err := repo.ListForRecheck(ctx, tenantID, now.Add(-7*24*time.Hour))
	if err != nil || len(recheck) != 1 {
		t.Errorf("ListForRecheck = %d, %v", len(recheck), err)
	}
	expired, err := repo.ListWithExpiredPreviousDKIM(ctx, tenantID, now.Add(time.Minute))
	if err != nil || len(expired) != 1 {
		t.Errorf("ListWithExpiredPreviousDKIM = %d, %v", len(expired), err)
	}
	if expired, _ := repo.ListWithExpiredPreviousDKIM(ctx, tenantID, now.Add(-time.Minute)); len(expired) != 0 {
		t.Error("una rotacion reciente no esta vencida")
	}

	list, total, err := repo.List(ctx, tenantID, 0, 10)
	if err != nil || total != 1 || len(list) != 1 {
		t.Errorf("List = %d/%d, %v", len(list), total, err)
	}

	pruned, err := repo.PruneChecks(ctx, tenantID, now.Add(-time.Minute))
	if err != nil || pruned != 1 {
		t.Errorf("PruneChecks = %d, %v", pruned, err)
	}

	if err := repo.Delete(ctx, tenantID, d.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := repo.Delete(ctx, tenantID, d.ID); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("segundo Delete: %v", err)
	}
	if left, _ := repo.LatestChecks(ctx, tenantID, d.ID); len(left) != 0 {
		t.Error("las comprobaciones caen con el dominio (ON DELETE CASCADE)")
	}
	if left, _ := repo.ListDKIMRotations(ctx, tenantID, d.ID, 10); len(left) != 0 {
		t.Error("el historial de claves cae con el dominio (ON DELETE CASCADE)")
	}
}

// Las claves solo cambian partiendo del selector que se vio; Update nunca las toca; cada marca
// DKIM va condicionada a su selector, y el historial guarda motivo, actor y selectores.
func TestRepositoryDKIMKeys(t *testing.T) {
	ctx, repo := setup(t)
	tenantID, actor := uuid.New(), uuid.New()
	d := sample(tenantID, "claves-"+uuid.NewString()[:8]+".test")
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	later := now.Add(time.Hour)

	next := *d
	next.DKIMSelector, next.DKIMPrivateKeyEnc, next.DKIMPublicKey = "cfm20260912", []byte{4}, "NEW"
	next.DKIMPreviousSelector, next.DKIMPreviousPrivateKeyEnc, next.DKIMPreviousPublicKey = "cfm202609", []byte{1, 2, 3}, "PUB"
	next.DKIMRotatedAt, next.DKIMPreviousSignedAt = &now, &now
	scheduled := &domain.DKIMRotation{
		TenantID: tenantID, DomainID: d.ID, Kind: domain.RotationScheduled,
		Selector: "cfm20260912", PreviousSelector: "cfm202609", ActorID: actor, RotatedAt: now,
	}
	if err := repo.SaveDKIMKeys(ctx, &next, "cfm199901", scheduled); !errors.Is(err, domain.ErrDKIMKeysChanged) {
		t.Fatalf("desde un selector que ya no es el actual: %v", err)
	}
	if err := repo.SaveDKIMKeys(ctx, &domain.Domain{ID: uuid.New(), TenantID: tenantID}, "cfm202609", scheduled); !errors.Is(err, domain.ErrDomainNotFound) {
		t.Errorf("dominio inexistente: %v", err)
	}
	if rs, _ := repo.ListDKIMRotations(ctx, tenantID, d.ID, 10); len(rs) != 0 {
		t.Fatalf("una rotacion rechazada no deja historial: %d", len(rs))
	}
	if err := repo.SaveDKIMKeys(ctx, &next, "cfm202609", scheduled); err != nil {
		t.Fatalf("SaveDKIMKeys: %v", err)
	}
	got, _ := repo.GetByID(ctx, tenantID, d.ID)
	if got.DKIMSelector != "cfm20260912" || got.DKIMPreviousSelector != "cfm202609" || got.DKIMPreviousSignedAt == nil ||
		!got.DKIMPreviousSignedAt.Equal(now) || got.DKIMConfirmedAt != nil || got.DKIMRevocationPending {
		t.Fatalf("claves guardadas: %+v", got)
	}

	stale := *d
	stale.Status = domain.StatusVerified
	if err := repo.Update(ctx, &stale); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ := repo.GetByID(ctx, tenantID, d.ID); got.DKIMSelector != "cfm20260912" || got.Status != domain.StatusVerified {
		t.Fatalf("Update con la fila de antes devolvio las claves viejas: %s/%s", got.DKIMSelector, got.Status)
	}

	for _, m := range []struct {
		selector string
		at       time.Time
	}{{"cfm202609", later}, {"cfm202609", now}, {"otro", later.Add(time.Hour)}} {
		if err := repo.MarkPreviousDKIMSigning(ctx, tenantID, d.ID, m.selector, m.at); err != nil {
			t.Fatalf("MarkPreviousDKIMSigning: %v", err)
		}
	}
	if got, _ := repo.GetByID(ctx, tenantID, d.ID); !got.DKIMPreviousSignedAt.Equal(later) {
		t.Errorf("la ultima firma solo avanza y solo con su selector: %v", got.DKIMPreviousSignedAt)
	}
	if expired, _ := repo.ListWithExpiredPreviousDKIM(ctx, tenantID, later); len(expired) != 0 {
		t.Error("la gracia cuenta desde la ultima firma, no desde la rotacion")
	}
	if expired, _ := repo.ListWithExpiredPreviousDKIM(ctx, tenantID, later.Add(time.Minute)); len(expired) != 1 {
		t.Error("vencida desde la ultima firma")
	}

	for _, c := range []struct {
		selector string
		at       time.Time
	}{{"cfm202609", now}, {"cfm20260912", now}, {"cfm20260912", later}} {
		if err := repo.ConfirmDKIM(ctx, tenantID, d.ID, c.selector, c.at); err != nil {
			t.Fatalf("ConfirmDKIM: %v", err)
		}
	}
	if got, _ := repo.GetByID(ctx, tenantID, d.ID); got.DKIMConfirmedAt == nil || !got.DKIMConfirmedAt.Equal(now) {
		t.Errorf("confirmada la primera vez y solo la actual: %v", got.DKIMConfirmedAt)
	}

	if err := repo.ClearPreviousDKIM(ctx, tenantID, d.ID, "otro"); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetByID(ctx, tenantID, d.ID); !got.HasPreviousDKIM() {
		t.Fatal("ClearPreviousDKIM de otro selector no retira la anterior")
	}
	if err := repo.ClearPreviousDKIM(ctx, tenantID, d.ID, "cfm202609"); err != nil {
		t.Fatal(err)
	}
	got, _ = repo.GetByID(ctx, tenantID, d.ID)
	if got.HasPreviousDKIM() || got.DKIMPreviousSignedAt != nil {
		t.Fatalf("anterior retirada: %+v", got)
	}

	revoked := *got
	revoked.DKIMSelector, revoked.DKIMPrivateKeyEnc, revoked.DKIMPublicKey = "cfm20260913", []byte{5}, "NEWER"
	revoked.DKIMConfirmedAt, revoked.DKIMRevocationPending = nil, true
	compromised := &domain.DKIMRotation{
		TenantID: tenantID, DomainID: d.ID, Kind: domain.RotationCompromised, Selector: "cfm20260913",
		RevokedSelectors: []string{"cfm20260912"}, Reason: "expuesta", ActorID: actor, RotatedAt: later,
	}
	if err := repo.SaveDKIMKeys(ctx, &revoked, "cfm20260912", compromised); err != nil {
		t.Fatalf("revocacion: %v", err)
	}
	if pending, _ := repo.ListPendingDKIMRevocation(ctx, tenantID); len(pending) != 1 || pending[0].ID != d.ID {
		t.Fatalf("pendientes = %d", len(pending))
	}
	if pending, _ := repo.ListPendingDKIMRevocation(ctx, uuid.New()); len(pending) != 0 {
		t.Error("otra empresa no ve las revocaciones de esta")
	}
	_ = repo.CompleteDKIMRevocation(ctx, tenantID, d.ID, "cfm20260912")
	if got, _ := repo.GetByID(ctx, tenantID, d.ID); !got.DKIMRevocationPending {
		t.Fatal("solo se completa la revocacion de la clave actual")
	}
	_ = repo.CompleteDKIMRevocation(ctx, tenantID, d.ID, "cfm20260913")
	if got, _ := repo.GetByID(ctx, tenantID, d.ID); got.DKIMRevocationPending {
		t.Fatal("revocacion completada")
	}

	// Una revocacion sin motivo no entra (CHECK) y la transaccion deshace tambien las claves.
	bad := revoked
	bad.DKIMSelector = "cfm20260914"
	if err := repo.SaveDKIMKeys(ctx, &bad, "cfm20260913", &domain.DKIMRotation{
		TenantID: tenantID, DomainID: d.ID, Kind: domain.RotationCompromised, Selector: "cfm20260914",
		RevokedSelectors: []string{"cfm20260913"}, RotatedAt: later,
	}); err == nil {
		t.Fatal("revocacion sin motivo aceptada")
	}
	if got, _ := repo.GetByID(ctx, tenantID, d.ID); got.DKIMSelector != "cfm20260913" {
		t.Fatalf("las claves cambiaron sin su historial: %s", got.DKIMSelector)
	}

	rs, err := repo.ListDKIMRotations(ctx, tenantID, d.ID, 10)
	if err != nil || len(rs) != 2 {
		t.Fatalf("historial = %d, %v", len(rs), err)
	}
	if rs[0].Kind != domain.RotationCompromised || rs[0].Reason != "expuesta" || rs[0].ActorID != actor ||
		len(rs[0].RevokedSelectors) != 1 || rs[0].RevokedSelectors[0] != "cfm20260912" || rs[0].PreviousSelector != "" {
		t.Errorf("revocacion = %+v", rs[0])
	}
	if rs[1].Kind != domain.RotationScheduled || rs[1].PreviousSelector != "cfm202609" || len(rs[1].RevokedSelectors) != 0 || rs[1].ActorID != actor {
		t.Errorf("rotacion = %+v", rs[1])
	}
	if one, _ := repo.ListDKIMRotations(ctx, tenantID, d.ID, 1); len(one) != 1 || one[0].ID != rs[0].ID {
		t.Error("el limite devuelve la mas reciente")
	}
	used, err := repo.UsedDKIMSelectors(ctx, tenantID, d.ID)
	sort.Strings(used)
	if err != nil || strings.Join(used, ",") != "cfm202609,cfm20260912,cfm20260913" {
		t.Errorf("selectores usados = %v, %v", used, err)
	}
}

// WithDKIMLock serializa por dominio y confirma o deshace a la vez las claves, el historial y el
// evento de la outbox.
func TestRepositoryDKIMLock(t *testing.T) {
	ctx, repo := setup(t)
	q := &db.ContextPool{}
	events := outboxadapter.NewPublisher(q)
	tenantID, actor := uuid.New(), uuid.New()
	d := sample(tenantID, "cerrojo-"+uuid.NewString()[:8]+".test")
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	outboxRows := func() int {
		var n int
		if err := q.QueryRow(ctx, `SELECT count(*) FROM platform.event_outbox
 WHERE subject = 'domains.domain.dkim_revoked' AND payload->'data'->>'domain_id' = $1`, d.ID.String()).Scan(&n); err != nil {
			t.Fatalf("outbox: %v", err)
		}
		return n
	}
	revoke := func(fail error) error {
		return repo.WithDKIMLock(ctx, tenantID, d.ID, func(ctx context.Context, fresh *domain.Domain) error {
			fresh.DKIMSelector, fresh.DKIMPrivateKeyEnc, fresh.DKIMPublicKey, fresh.DKIMRevocationPending = "cfm20260912", []byte{7}, "NEW", true
			rot := &domain.DKIMRotation{
				TenantID: tenantID, DomainID: d.ID, Kind: domain.RotationCompromised, Selector: "cfm20260912",
				RevokedSelectors: []string{"cfm202609"}, Reason: "expuesta", ActorID: actor, RotatedAt: time.Now().UTC(),
			}
			if err := repo.SaveDKIMKeys(ctx, fresh, "cfm202609", rot); err != nil {
				return err
			}
			if err := events.DKIMRevoked(ctx, fresh, rot); err != nil {
				return err
			}
			return fail
		})
	}

	errCut := errors.New("corte antes de confirmar")
	if err := revoke(errCut); !errors.Is(err, errCut) {
		t.Fatalf("WithDKIMLock: %v", err)
	}
	got, _ := repo.GetByID(ctx, tenantID, d.ID)
	rs, _ := repo.ListDKIMRotations(ctx, tenantID, d.ID, 10)
	if got.DKIMSelector != "cfm202609" || len(rs) != 0 || outboxRows() != 0 {
		t.Fatalf("deshecho a medias: selector %s, historial %d, outbox %d", got.DKIMSelector, len(rs), outboxRows())
	}
	if err := revoke(nil); err != nil {
		t.Fatalf("WithDKIMLock: %v", err)
	}
	got, _ = repo.GetByID(ctx, tenantID, d.ID)
	rs, _ = repo.ListDKIMRotations(ctx, tenantID, d.ID, 10)
	if got.DKIMSelector != "cfm20260912" || !got.DKIMRevocationPending || len(rs) != 1 || outboxRows() != 1 {
		t.Fatalf("confirmado a medias: selector %s, historial %d, outbox %d", got.DKIMSelector, len(rs), outboxRows())
	}
	var userID, reason, hosts string
	if err := q.QueryRow(ctx, `SELECT payload->>'user_id', payload->'data'->>'reason', payload->'data'->'remove_dns_records'->>0
 FROM platform.event_outbox WHERE subject = 'domains.domain.dkim_revoked' AND payload->'data'->>'domain_id' = $1`, d.ID.String()).Scan(&userID, &reason, &hosts); err != nil {
		t.Fatal(err)
	}
	if userID != actor.String() || reason != "expuesta" || hosts != "cfm202609._domainkey."+d.Domain {
		t.Errorf("evento: usuario %s, motivo %s, TXT %s", userID, reason, hosts)
	}

	entered, release := make(chan struct{}), make(chan struct{})
	firstDone, second := make(chan error, 1), make(chan error, 1)
	go func() {
		firstDone <- repo.WithDKIMLock(ctx, tenantID, d.ID, func(context.Context, *domain.Domain) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	go func() {
		second <- repo.WithDKIMLock(ctx, tenantID, d.ID, func(context.Context, *domain.Domain) error { return nil })
	}()
	select {
	case err := <-second:
		t.Fatalf("entro con el cerrojo del dominio tomado: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	for _, ch := range []chan error{firstDone, second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("WithDKIMLock: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("el cerrojo no se solto al terminar la transaccion")
		}
	}
}

// Los registros MTA-STS y TLS-RPT se guardan en dns_checks (migracion 06); el CHECK sigue rechazando
// un tipo que el servicio no comprueba.
func TestRepositoryGuardaLosChecksDeMTASTSYTLSRPT(t *testing.T) {
	ctx, repo := setup(t)
	tenantID := uuid.New()
	d := sample(tenantID, "sts-"+uuid.NewString()[:8]+".test")
	if err := repo.Create(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	check := func(kind domain.RecordKind) domain.DNSCheck {
		return domain.DNSCheck{TenantID: tenantID, DomainID: d.ID, CheckedAt: now, Record: kind, Expected: "v=x", OK: true}
	}
	if err := repo.SaveChecks(ctx, []domain.DNSCheck{check(domain.RecordMTASTS), check(domain.RecordTLSRPT)}); err != nil {
		t.Fatalf("SaveChecks: %v", err)
	}
	latest, err := repo.LatestChecks(ctx, tenantID, d.ID)
	if err != nil || len(latest) != 2 {
		t.Fatalf("LatestChecks = %d, %v", len(latest), err)
	}
	if err := repo.SaveChecks(ctx, []domain.DNSCheck{check("dnssec")}); err == nil {
		t.Error("un tipo de registro que el servicio no comprueba debe rechazarse")
	}
}
