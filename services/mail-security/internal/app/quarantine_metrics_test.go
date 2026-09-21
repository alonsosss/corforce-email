package app

import (
	"context"
	"errors"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// countingQuarantine cuenta cada desenlace de la cuarentena.
type countingQuarantine struct{ stored, released, discarded, learned, learnedHam int }

func (c *countingQuarantine) QuarantineStored()      { c.stored++ }
func (c *countingQuarantine) QuarantineReleased()    { c.released++ }
func (c *countingQuarantine) QuarantineDiscarded()   { c.discarded++ }
func (c *countingQuarantine) QuarantineLearnedSpam() { c.learned++ }
func (c *countingQuarantine) QuarantineLearnedHam()  { c.learnedHam++ }

type learnerFalso struct {
	err error
	// ham anota los mensajes con los que se entreno como legitimos.
	ham *[][]byte
}

func (l learnerFalso) LearnSpam(context.Context, []byte) error { return l.err }

func (l learnerFalso) LearnHam(_ context.Context, msg []byte) error {
	if l.err != nil {
		return l.err
	}
	if l.ham != nil {
		*l.ham = append(*l.ham, msg)
	}
	return nil
}

func newMeteredQuarantine(t *testing.T) (*QuarantineUseCase, *countingQuarantine, *apptest.Quarantine, *reinyectorFalso, *apptest.Publisher) {
	t.Helper()
	tx := &apptest.Tx{}
	q := &apptest.Quarantine{}
	pub := &apptest.Publisher{Tx: tx}
	reinj := &reinyectorFalso{}
	metrics := &countingQuarantine{}
	uc := NewQuarantineUseCase(QuarantineDeps{Tx: tx, Repo: q, Reinjector: reinj, Events: pub, Learner: learnerFalso{}, Metrics: metrics, Logger: zap.NewNop()})
	return uc, metrics, q, reinj, pub
}

func sampleItem(tenant uuid.UUID) domain.QuarantineItem {
	return domain.QuarantineItem{ID: uuid.New(), TenantID: tenant, Rcpt: "ana@acme.com", Sender: "x@y.com", Score: decimal.NewFromInt(12), Msg: []byte("m")}
}

func TestLiberarUnMensajeCuentaUnFalsoPositivoSoloSiSeLibera(t *testing.T) {
	uc, metrics, q, reinj, _ := newMeteredQuarantine(t)
	tenant := uuid.New()
	item := sampleItem(tenant)
	q.Items = []domain.QuarantineItem{item}

	reinj.fallo = errors.New("postfix no responde")
	if err := uc.Release(context.Background(), tenant, item.ID, "u1"); err == nil {
		t.Fatal("sin reinyeccion no hay liberacion")
	}
	if metrics.released != 0 {
		t.Fatalf("una liberacion fallida no cuenta: %+v", metrics)
	}
	reinj.fallo = nil
	if err := uc.Release(context.Background(), tenant, item.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	if metrics.released != 1 || metrics.discarded != 0 {
		t.Fatalf("una liberacion cuenta una vez: %+v", metrics)
	}
	if err := uc.Release(context.Background(), tenant, item.ID, "u1"); err == nil || metrics.released != 1 {
		t.Fatalf("la segunda ya no encuentra el mensaje y no suma: %v %+v", err, metrics)
	}
}

func TestDescartarYEntrenarComoSpamSeCuentanAparte(t *testing.T) {
	uc, metrics, q, _, _ := newMeteredQuarantine(t)
	tenant := uuid.New()
	a, b := sampleItem(tenant), sampleItem(tenant)
	q.Items = []domain.QuarantineItem{a, b}

	if err := uc.LearnSpam(context.Background(), tenant, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := uc.Delete(context.Background(), tenant, b.ID); err != nil {
		t.Fatal(err)
	}
	if metrics.learned != 1 || metrics.discarded != 1 || metrics.released != 0 {
		t.Fatalf("%+v", metrics)
	}
	if err := uc.Delete(context.Background(), tenant, b.ID); err == nil || metrics.discarded != 1 {
		t.Fatalf("descartar lo que ya no esta no suma: %v %+v", err, metrics)
	}
}

func TestUnEntrenamientoFallidoNoSeCuenta(t *testing.T) {
	uc, metrics, q, _, _ := newMeteredQuarantine(t)
	uc.learner = learnerFalso{err: errors.New("controller caido")}
	tenant := uuid.New()
	item := sampleItem(tenant)
	q.Items = []domain.QuarantineItem{item}
	if err := uc.LearnSpam(context.Background(), tenant, item.ID); err == nil || metrics.learned != 0 {
		t.Fatalf("%v %+v", err, metrics)
	}
}

func TestSinMetricasElCasoDeUsoFunciona(t *testing.T) {
	tx := &apptest.Tx{}
	q := &apptest.Quarantine{}
	uc := NewQuarantineUseCase(QuarantineDeps{Tx: tx, Repo: q, Reinjector: &reinyectorFalso{}, Events: &apptest.Publisher{Tx: tx}, Logger: zap.NewNop()})
	tenant := uuid.New()
	item := sampleItem(tenant)
	q.Items = []domain.QuarantineItem{item}
	if err := uc.Delete(context.Background(), tenant, item.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRetenerUnMensajeCuentaLoGuardado(t *testing.T) {
	f := newEngineFixture()
	metrics := &countingQuarantine{}
	logger := zap.NewNop()
	f.uc = NewEngineUseCase(EngineDeps{
		Tx: f.tx, Documents: f.docs, Directory: f.dir, Policy: f.policy, Quarantine: f.q,
		Sync:  NewRedisSync(f.store, f.dir, f.policy, logger),
		Store: f.store, Events: f.pub, Metrics: metrics, Logger: logger, LogLines: 3,
	})
	tenant := uuid.New()
	f.dir.Mailboxes["ana@acme.com"] = domain.Mailbox{TenantID: tenant, Username: "ana@acme.com", Domain: "acme.com", Active: 1}
	small := domain.DefaultQuarantineSettings(tenant)
	small.MaxSizeBytes = 5
	f.policy.QSettings[tenant] = small

	meta := domain.QuarantineMetadata{QID: "Q1", Rcpt: []string{"ana@acme.com"}, From: "spam@x.com", Score: decimal.NewFromInt(12)}
	if _, err := f.uc.Pipe(context.Background(), meta, []byte("largo de mas")); err != nil {
		t.Fatal(err)
	}
	if metrics.stored != 0 {
		t.Fatalf("lo que se omite por tamano no se cuenta como retenido: %+v", metrics)
	}
	small.MaxSizeBytes = 1 << 20
	f.policy.QSettings[tenant] = small
	if _, err := f.uc.Pipe(context.Background(), meta, []byte("cabe")); err != nil {
		t.Fatal(err)
	}
	if metrics.stored != 1 {
		t.Fatalf("retenido: %+v", metrics)
	}
}

func TestLiberarComoLegitimoLiberaYEntrenaConElMensajeLiberado(t *testing.T) {
	uc, metrics, q, _, _ := newMeteredQuarantine(t)
	var ham [][]byte
	uc.learner = learnerFalso{ham: &ham}
	tenant := uuid.New()
	item := sampleItem(tenant)
	item.Msg = []byte("From: ana@acme.com\r\n\r\nfactura")
	q.Items = []domain.QuarantineItem{item}

	if err := uc.ReleaseAndLearnHam(context.Background(), tenant, item.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	if len(q.Items) != 0 || metrics.released != 1 || metrics.learnedHam != 1 || metrics.learned != 0 {
		t.Fatalf("liberado y aprendido: filas=%d %+v", len(q.Items), metrics)
	}
	if len(ham) != 1 || string(ham[0]) != string(item.Msg) {
		t.Fatalf("se entrena con el mensaje que se libero: %q", ham)
	}
}

func TestSiElEntrenamientoFallaElMensajeQuedaLiberadoYNoSeCuenta(t *testing.T) {
	uc, metrics, q, reinj, _ := newMeteredQuarantine(t)
	uc.learner = learnerFalso{err: errors.New("controller caido")}
	tenant := uuid.New()
	item := sampleItem(tenant)
	q.Items = []domain.QuarantineItem{item}

	if err := uc.ReleaseAndLearnHam(context.Background(), tenant, item.ID, "u1"); err != nil {
		t.Fatalf("el dueno ya tiene su correo: no es un error: %v", err)
	}
	if reinj.entregados != 1 || len(q.Items) != 0 || metrics.released != 1 || metrics.learnedHam != 0 {
		t.Fatalf("entregas=%d filas=%d %+v", reinj.entregados, len(q.Items), metrics)
	}
}

func TestUnaLiberacionFallidaNoEntrenaNada(t *testing.T) {
	uc, metrics, q, reinj, _ := newMeteredQuarantine(t)
	var ham [][]byte
	uc.learner = learnerFalso{ham: &ham}
	reinj.fallo = errors.New("postfix no responde")
	tenant := uuid.New()
	item := sampleItem(tenant)
	q.Items = []domain.QuarantineItem{item}

	if err := uc.ReleaseAndLearnHam(context.Background(), tenant, item.ID, "u1"); err == nil {
		t.Fatal("sin reinyeccion no hay liberacion")
	}
	if len(ham) != 0 || metrics.learnedHam != 0 || len(q.Items) != 1 {
		t.Fatalf("no se entrena con lo que no se libero: %v %+v", ham, metrics)
	}
}

func TestSinControllerNoSeLiberaNiSeEntrena(t *testing.T) {
	tx := &apptest.Tx{}
	q := &apptest.Quarantine{}
	reinj := &reinyectorFalso{}
	uc := NewQuarantineUseCase(QuarantineDeps{Tx: tx, Repo: q, Reinjector: reinj, Events: &apptest.Publisher{Tx: tx}, Logger: zap.NewNop()})
	tenant := uuid.New()
	item := sampleItem(tenant)
	q.Items = []domain.QuarantineItem{item}
	if err := uc.ReleaseAndLearnHam(context.Background(), tenant, item.ID, "u1"); !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("%v", err)
	}
	if reinj.entregados != 0 || len(q.Items) != 1 {
		t.Fatal("sin controller configurado no se libera a medias")
	}
}
