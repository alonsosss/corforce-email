package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/mail-files/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type harness struct {
	uc      *UseCase
	repo    *memRepo
	store   *memStore
	scanner *fakeScanner
	spool   *memSpool
	binder  *binder
	links   *domain.LinkSigner
	now     time.Time
	owner   domain.Owner
}

func testConfig() Config {
	return Config{
		Policy: domain.Policy{MaxFileBytes: 1 << 10, DefaultExpiryDays: 7, MaxExpiryDays: 30, DefaultMaxDownloads: 2, MaxDownloads: 5,
			MailboxQuotaBytes: 4 << 10, TenantQuotaBytes: 6 << 10, MaxActivePerMailbox: 10},
		MaxConcurrentUploads: 2, ListLimit: 50, PendingGrace: time.Hour, DeleteGrace: 30 * time.Minute,
		HistoryRetention: 24 * time.Hour, SweepInterval: time.Minute, SweepBatch: 100,
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	links, err := domain.NewLinkSigner(strings.Repeat("s", 32), "https://correo.example.com")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{
		repo: newMemRepo(), store: newMemStore(), scanner: &fakeScanner{}, spool: &memSpool{},
		binder: &binder{unknown: map[uuid.UUID]bool{}}, links: links,
		now:   time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC),
		owner: domain.Owner{TenantID: uuid.New(), MailboxID: uuid.New()},
	}
	h.uc, err = New(Deps{
		Repo: h.repo, Binder: h.binder, Tenants: tenants{h.owner.TenantID}, Store: h.store, Scanner: h.scanner,
		Spool: h.spool, Links: links, Now: func() time.Time { return h.now }, Config: testConfig(), Logger: zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) upload(t *testing.T, name, content string, opts domain.UploadOptions) domain.SharedFile {
	t.Helper()
	f, err := h.uc.Upload(context.Background(), h.owner, name, strings.NewReader(content), opts)
	if err != nil {
		t.Fatalf("subida de %q: %v", name, err)
	}
	return f
}

// claimsOf lee del enlace lo que leeria la ruta publica.
func claimsOf(t *testing.T, raw string) (domain.LinkClaims, string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	segs := strings.Split(strings.TrimPrefix(u.Path, domain.DownloadPath+"/"), "/")
	c, sig, err := domain.ParseLink(segs[0], segs[1], u.Query().Get("x"), u.Query().Get("s"))
	if err != nil {
		t.Fatalf("enlace ilegible %s: %v", raw, err)
	}
	return c, sig
}

func TestSubidaAnalizaGuardaYDevuelveElEnlace(t *testing.T) {
	h := newHarness(t)
	content := "planos del edificio"
	f := h.upload(t, "../planos\u202E.dwg", content, domain.UploadOptions{ExpiresInDays: 3, MaxDownloads: 4})

	sum := sha256.Sum256([]byte(content))
	if f.Name != "planos.dwg" || f.SizeBytes != int64(len(content)) || f.SHA256 != hex.EncodeToString(sum[:]) ||
		f.Status != domain.StatusReady || f.MaxDownloads != 4 || !f.ExpiresAt.Equal(h.now.Add(72*time.Hour)) {
		t.Fatalf("fichero: %+v", f.File)
	}
	if h.scanner.scanned != 1 || h.spool.open != 0 {
		t.Fatalf("analisis %d, temporales abiertos %d", h.scanner.scanned, h.spool.open)
	}
	if got := string(h.store.objects[f.ObjectKey]); got != content {
		t.Fatalf("almacen: %q", got)
	}
	if !strings.HasPrefix(f.ObjectKey, "private/"+h.owner.TenantID.String()+"/") {
		t.Fatalf("clave: %s", f.ObjectKey)
	}
	c, sig := claimsOf(t, f.URL)
	if c.TenantID != h.owner.TenantID || c.FileID != f.ID || !c.ExpiresAt.Equal(f.ExpiresAt) || !h.links.Verify(c, sig) {
		t.Fatalf("enlace: %s", f.URL)
	}
}

func TestSubidaConEICARSeRechazaSinGuardarNada(t *testing.T) {
	h := newHarness(t)
	_, err := h.uc.Upload(context.Background(), h.owner, "factura.pdf", strings.NewReader("cabecera "+eicar), domain.UploadOptions{})
	if !errors.Is(err, domain.ErrInfected) {
		t.Fatalf("EICAR: %v", err)
	}
	if len(h.repo.files) != 0 || len(h.store.objects) != 0 || h.spool.open != 0 {
		t.Fatalf("se guardo algo: filas %d objetos %d temporales %d", len(h.repo.files), len(h.store.objects), h.spool.open)
	}
}

func TestSinVeredictoDeClamAVNoSeGuardaNada(t *testing.T) {
	h := newHarness(t)
	h.scanner.down = true
	_, err := h.uc.Upload(context.Background(), h.owner, "a.txt", strings.NewReader("hola"), domain.UploadOptions{})
	if !errors.Is(err, domain.ErrScanUnavailable) || len(h.repo.files) != 0 || len(h.store.objects) != 0 {
		t.Fatalf("clamd caido: %v", err)
	}
}

func TestSubidaRechazaTamanosYOpcionesFueraDePolitica(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.uc.Upload(ctx, h.owner, "a.bin", strings.NewReader(strings.Repeat("x", 1<<10+1)), domain.UploadOptions{}); !errors.Is(err, domain.ErrTooLarge) {
		t.Fatalf("grande: %v", err)
	}
	if _, err := h.uc.Upload(ctx, h.owner, "a.bin", strings.NewReader(""), domain.UploadOptions{}); !errors.Is(err, domain.ErrEmpty) {
		t.Fatalf("vacio: %v", err)
	}
	if _, err := h.uc.Upload(ctx, h.owner, "a.bin", &failingReader{}, domain.UploadOptions{}); !errors.Is(err, domain.ErrIncomplete) {
		t.Fatalf("cortada: %v", err)
	}
	var verr *domain.ValidationError
	if _, err := h.uc.Upload(ctx, h.owner, "a.bin", strings.NewReader("x"), domain.UploadOptions{ExpiresInDays: 31}); !errors.As(err, &verr) {
		t.Fatalf("caducidad: %v", err)
	}
	if _, err := h.uc.Upload(ctx, h.owner, "../", strings.NewReader("x"), domain.UploadOptions{}); !errors.As(err, &verr) {
		t.Fatalf("nombre: %v", err)
	}
	if h.scanner.scanned != 0 || len(h.repo.files) != 0 || h.spool.open != 0 {
		t.Fatalf("se analizo o guardo algo rechazado: %d %d %d", h.scanner.scanned, len(h.repo.files), h.spool.open)
	}
}

func TestCuotaDelBuzonYDeLaEmpresa(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chunk := strings.Repeat("x", 1<<10)
	for i := 0; i < 4; i++ {
		h.upload(t, "parte.bin", chunk, domain.UploadOptions{})
	}
	if _, err := h.uc.Upload(ctx, h.owner, "otra.bin", strings.NewReader("x"), domain.UploadOptions{}); !errors.Is(err, domain.ErrMailboxQuota) {
		t.Fatalf("buzon lleno: %v", err)
	}
	colleague := domain.Owner{TenantID: h.owner.TenantID, MailboxID: uuid.New()}
	for i := 0; i < 2; i++ {
		if _, err := h.uc.Upload(ctx, colleague, "c.bin", strings.NewReader(chunk), domain.UploadOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.uc.Upload(ctx, colleague, "c.bin", strings.NewReader("x"), domain.UploadOptions{}); !errors.Is(err, domain.ErrTenantQuota) {
		t.Fatalf("empresa llena: %v", err)
	}
	// Revocar libera el espacio en el acto.
	list, _ := h.uc.List(ctx, h.owner)
	if _, err := h.uc.Revoke(ctx, h.owner, list.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.uc.Upload(ctx, colleague, "c.bin", strings.NewReader("x"), domain.UploadOptions{}); err != nil {
		t.Fatalf("tras revocar: %v", err)
	}
}

func TestSubidasSimultaneasAcotadas(t *testing.T) {
	h := newHarness(t)
	h.uc.slots <- struct{}{}
	h.uc.slots <- struct{}{}
	if _, err := h.uc.Upload(context.Background(), h.owner, "a.txt", strings.NewReader("x"), domain.UploadOptions{}); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("sin ranura: %v", err)
	}
}

func TestSinAlmacenLaFuncionEstaApagada(t *testing.T) {
	h := newHarness(t)
	h.uc.store = nil
	if h.uc.Enabled() {
		t.Fatal("habilitado sin almacen")
	}
	if _, err := h.uc.Upload(context.Background(), h.owner, "a.txt", strings.NewReader("x"), domain.UploadOptions{}); !errors.Is(err, domain.ErrStorageDisabled) {
		t.Fatalf("subida: %v", err)
	}
}

func TestFalloDelAlmacenLiberaLaCuota(t *testing.T) {
	h := newHarness(t)
	h.store.putErr = errors.New("minio caido")
	_, err := h.uc.Upload(context.Background(), h.owner, "a.txt", strings.NewReader("hola"), domain.UploadOptions{})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("almacen caido: %v", err)
	}
	for _, f := range h.repo.files {
		if f.Status != domain.StatusFailed {
			t.Fatalf("la fila quedo %s", f.Status)
		}
	}
	if u, _ := h.repo.Usage(context.Background(), h.owner, h.now); u.MailboxBytes != 0 {
		t.Fatalf("cuota ocupada: %+v", u)
	}
}

func readAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDescargaCuentaHastaElTopeYLaPaginaNoGasta(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	f := h.upload(t, "contrato.pdf", "contenido", domain.UploadOptions{MaxDownloads: 2})
	c, sig := claimsOf(t, f.URL)

	for i := 0; i < 3; i++ {
		if got, err := h.uc.Inspect(ctx, c, sig); err != nil || got.Downloads != 0 {
			t.Fatalf("la pagina gasta descargas: %v %+v", err, got)
		}
	}
	for i := 1; i <= 2; i++ {
		got, rc, err := h.uc.Download(ctx, c, sig)
		if err != nil || got.Downloads != i || readAll(t, rc) != "contenido" {
			t.Fatalf("descarga %d: %v %+v", i, err, got)
		}
	}
	if _, _, err := h.uc.Download(ctx, c, sig); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("pasado el tope: %v", err)
	}
	if _, err := h.uc.Inspect(ctx, c, sig); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("pagina de un enlace agotado: %v", err)
	}
}

func TestEnlaceCaducadoAlteradoORevocadoNoSirve(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	f := h.upload(t, "a.txt", "hola", domain.UploadOptions{ExpiresInDays: 1})
	c, sig := claimsOf(t, f.URL)

	tampered := c
	tampered.ExpiresAt = c.ExpiresAt.Add(24 * time.Hour)
	if _, _, err := h.uc.Download(ctx, tampered, sig); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("caducidad alargada: %v", err)
	}
	// Una firma valida con otra caducidad no vale: la fila manda.
	forged := domain.LinkClaims{TenantID: c.TenantID, FileID: c.FileID, ExpiresAt: c.ExpiresAt.Add(time.Hour)}
	if _, err := h.uc.Inspect(ctx, forged, h.links.Sign(forged)); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("caducidad distinta de la guardada: %v", err)
	}
	otherTenant := domain.LinkClaims{TenantID: uuid.New(), FileID: c.FileID, ExpiresAt: c.ExpiresAt}
	if _, err := h.uc.Inspect(ctx, otherTenant, h.links.Sign(otherTenant)); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("fichero de otra empresa: %v", err)
	}
	h.binder.unknown[otherTenant.TenantID] = true
	if _, err := h.uc.Inspect(ctx, otherTenant, h.links.Sign(otherTenant)); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("empresa desconocida: %v", err)
	}

	h.now = f.ExpiresAt
	if _, _, err := h.uc.Download(ctx, c, sig); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("caducado: %v", err)
	}

	h.now = f.CreatedAt
	if _, err := h.uc.Revoke(ctx, h.owner, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.uc.Download(ctx, c, sig); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("revocado: %v", err)
	}
	if _, ok := h.store.objects[f.ObjectKey]; ok {
		t.Fatal("el objeto sigue tras revocar")
	}
	if _, err := h.uc.Revoke(ctx, h.owner, f.ID); !errors.Is(err, domain.ErrNotRevocable) {
		t.Fatalf("revocar dos veces: %v", err)
	}
	if _, err := h.uc.Revoke(ctx, domain.Owner{TenantID: h.owner.TenantID, MailboxID: uuid.New()}, f.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revocar el de un companero: %v", err)
	}
}

func TestObjetoDesaparecidoNoGastaDescarga(t *testing.T) {
	h := newHarness(t)
	f := h.upload(t, "a.txt", "hola", domain.UploadOptions{})
	delete(h.store.objects, f.ObjectKey)
	c, sig := claimsOf(t, f.URL)
	if _, _, err := h.uc.Download(context.Background(), c, sig); !errors.Is(err, domain.ErrLinkInvalid) {
		t.Fatalf("sin objeto: %v", err)
	}
	if got, _ := h.repo.Get(context.Background(), h.owner.TenantID, f.ID); got.Downloads != 0 {
		t.Fatalf("se conto una descarga: %d", got.Downloads)
	}
}

func TestListadoSoloEntregaElEnlaceVigente(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	active := h.upload(t, "a.txt", "a", domain.UploadOptions{})
	revoked := h.upload(t, "b.txt", "b", domain.UploadOptions{})
	if _, err := h.uc.Revoke(ctx, h.owner, revoked.ID); err != nil {
		t.Fatal(err)
	}
	list, err := h.uc.List(ctx, h.owner)
	if err != nil || len(list.Items) != 2 || list.Usage.MailboxActive != 1 || list.Usage.MailboxBytes != 1 {
		t.Fatalf("listado: %v %+v", err, list.Usage)
	}
	for _, item := range list.Items {
		if item.ID == active.ID && item.URL == "" {
			t.Fatal("el vigente llega sin enlace")
		}
	}
	other, _ := h.uc.List(ctx, domain.Owner{TenantID: h.owner.TenantID, MailboxID: uuid.New()})
	if len(other.Items) != 0 {
		t.Fatal("un companero ve los enlaces de otro buzon")
	}
}

func TestBarridoCierraBorraYPoda(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	expiring := h.upload(t, "caduca.txt", "a", domain.UploadOptions{ExpiresInDays: 1})
	exhausted := h.upload(t, "agota.txt", "b", domain.UploadOptions{ExpiresInDays: 30, MaxDownloads: 1})
	kept := h.upload(t, "sigue.txt", "c", domain.UploadOptions{ExpiresInDays: 30})
	c, sig := claimsOf(t, exhausted.URL)
	if _, rc, err := h.uc.Download(ctx, c, sig); err != nil {
		t.Fatal(err)
	} else {
		_ = rc.Close()
	}
	stale := domain.File{ID: uuid.New(), TenantID: h.owner.TenantID, MailboxID: h.owner.MailboxID, Name: "x", SizeBytes: 1,
		ObjectKey: "private/x", Status: domain.StatusPending, ExpiresAt: h.now.Add(time.Hour), MaxDownloads: 1, CreatedAt: h.now}
	h.repo.files[stale.ID] = stale

	// Justo tras caducar: se cierra, pero su objeto espera la gracia de borrado. El agotado hace un dia
	// y la subida abandonada ya se borran.
	h.now = expiring.ExpiresAt.Add(time.Minute)
	r := h.uc.Sweep(ctx)
	if r.Expired != 1 || r.Failed != 1 || r.ObjectsDeleted != 2 || r.Tenants != 1 || r.Errors != 0 {
		t.Fatalf("primera pasada: %+v", r)
	}
	if _, ok := h.store.objects[expiring.ObjectKey]; !ok {
		t.Fatal("se borro un objeto dentro de la gracia")
	}
	h.now = h.now.Add(time.Hour)
	r = h.uc.Sweep(ctx)
	if r.ObjectsDeleted != 1 {
		t.Fatalf("segunda pasada: %+v", r)
	}
	for _, key := range []string{expiring.ObjectKey, exhausted.ObjectKey} {
		if _, ok := h.store.objects[key]; ok {
			t.Fatalf("sigue %s", key)
		}
	}
	if _, ok := h.store.objects[kept.ObjectKey]; !ok {
		t.Fatal("se borro uno vigente")
	}
	if r = h.uc.Sweep(ctx); r.ObjectsDeleted != 0 {
		t.Fatalf("la pasada repite borrados: %+v", r)
	}
}

func TestNewRechazaConfiguracionIncoherente(t *testing.T) {
	h := newHarness(t)
	cfg := testConfig()
	cfg.Policy.DefaultMaxDownloads = 99
	if _, err := New(Deps{Repo: h.repo, Binder: h.binder, Scanner: h.scanner, Spool: h.spool, Links: h.links, Config: cfg, Logger: zap.NewNop()}); err == nil {
		t.Fatal("politica incoherente aceptada")
	}
	if _, err := New(Deps{Config: testConfig(), Logger: zap.NewNop()}); err == nil {
		t.Fatal("sin dependencias aceptado")
	}
}
