package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeAnchors struct {
	heads        map[domain.ChainName]*domain.ChainHead
	found        map[domain.ChainName]domain.AnchorFindings
	saved        []*domain.ChainAnchor
	alreadySaved bool
	headErr      error
	findErr      error
	saveErr      error
}

func (f *fakeAnchors) Head(_ context.Context, c domain.ChainName) (*domain.ChainHead, error) {
	return f.heads[c], f.headErr
}

func (f *fakeAnchors) Findings(_ context.Context, c domain.ChainName) (domain.AnchorFindings, error) {
	return f.found[c], f.findErr
}

func (f *fakeAnchors) Save(_ context.Context, a *domain.ChainAnchor) (bool, error) {
	if f.saveErr != nil || f.alreadySaved {
		return false, f.saveErr
	}
	a.AnchoredAt = time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	f.saved = append(f.saved, a)
	return true, nil
}

type fakeAnchorEvents struct {
	anchored []*domain.ChainAnchor
	err      error
}

func (f *fakeAnchorEvents) ChainAnchored(_ context.Context, a *domain.ChainAnchor) error {
	if f.err != nil {
		return f.err
	}
	f.anchored = append(f.anchored, a)
	return nil
}

type passthroughTx struct{ calls int }

func (t *passthroughTx) Transact(ctx context.Context, fn func(context.Context) error) error {
	t.calls++
	return fn(ctx)
}

var (
	_ ports.ChainAnchorRepository = (*fakeAnchors)(nil)
	_ ports.ChainAnchorPublisher  = (*fakeAnchorEvents)(nil)
	_ ports.Transactor            = (*passthroughTx)(nil)
)

type anchorRig struct {
	uc      *AuditUseCase
	logs    *recordedLogs
	sec     *fakeSecurity
	anchors *fakeAnchors
	events  *fakeAnchorEvents
	tx      *passthroughTx
}

func newAnchorRig() *anchorRig {
	r := &anchorRig{
		logs:    &recordedLogs{verified: &domain.ChainIntegrity{OK: true, Chain: domain.ChainAuditLogs}},
		sec:     &fakeSecurity{},
		anchors: &fakeAnchors{heads: map[domain.ChainName]*domain.ChainHead{}, found: map[domain.ChainName]domain.AnchorFindings{}},
		events:  &fakeAnchorEvents{},
		tx:      &passthroughTx{},
	}
	r.uc = NewAuditUseCase(AuditDeps{
		Logs: r.logs, Security: r.sec, Events: &fakePublisher{}, Logger: zap.NewNop(),
		Anchors: r.anchors, AnchorEvents: r.events, Tx: r.tx,
	})
	return r
}

func anchorAt(chain domain.ChainName, seq int64, hash string) *domain.ChainAnchor {
	return &domain.ChainAnchor{Chain: chain, HeadSeq: seq, HeadHash: hash, HashVersion: 2}
}

func TestCheckAnchorsDistingueCabezaCortaDeHashDistinto(t *testing.T) {
	last := anchorAt(domain.ChainAuditLogs, 10, "h10")
	casos := []struct {
		nombre string
		head   *domain.ChainHead
		found  domain.AnchorFindings
		want   string
	}{
		{"sin anclas", &domain.ChainHead{Seq: 3}, domain.AnchorFindings{}, ""},
		{"cadena vacia sin anclas", nil, domain.AnchorFindings{}, ""},
		{"contiene el ancla", &domain.ChainHead{Seq: 12}, domain.AnchorFindings{Last: last}, ""},
		{"cabeza en el ancla", &domain.ChainHead{Seq: 10}, domain.AnchorFindings{Last: last}, ""},
		{"cabeza mas corta", &domain.ChainHead{Seq: 9}, domain.AnchorFindings{Last: last}, domain.ReasonHeadBehindAnchor},
		{"cadena vaciada", nil, domain.AnchorFindings{Last: last}, domain.ReasonHeadBehindAnchor},
		{"hash distinto", &domain.ChainHead{Seq: 12}, domain.AnchorFindings{Last: last, Mismatched: last}, domain.ReasonAnchorMismatch},
		{"la cabeza corta manda sobre el hash", &domain.ChainHead{Seq: 9}, domain.AnchorFindings{Last: last, Mismatched: last}, domain.ReasonHeadBehindAnchor},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := domain.CheckAnchors(c.head, c.found); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestVerificarUnaCadenaIntactaConAnclaSigueIntacta(t *testing.T) {
	r := newAnchorRig()
	last := anchorAt(domain.ChainAuditLogs, 5, "h5")
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 8, Hash: "h8"}
	r.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: last}
	res, err := r.uc.VerifyChainIntegrity(context.Background(), uuid.New())
	if err != nil || !res.OK || res.Anchor != last || res.Reason != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if res.SecurityEvents == nil || !res.SecurityEvents.OK {
		t.Fatalf("falta el resultado de la cadena de eventos: %+v", res.SecurityEvents)
	}
}

func TestVerificarDetectaLaCabezaMasCortaQueElAncla(t *testing.T) {
	r := newAnchorRig()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 4, Hash: "h4"}
	r.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: anchorAt(domain.ChainAuditLogs, 9, "h9")}
	res, err := r.uc.VerifyChainIntegrity(context.Background(), uuid.New())
	if err != nil || res.OK || res.Reason != domain.ReasonHeadBehindAnchor || res.Chain != domain.ChainAuditLogs {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestUnaRoturaDeFilasNoLaPisaElAncla(t *testing.T) {
	r := newAnchorRig()
	id := uuid.New()
	r.logs.verified = &domain.ChainIntegrity{OK: false, Chain: domain.ChainAuditLogs, Reason: domain.ReasonChainBroken, BrokenID: &id}
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 4}
	r.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: anchorAt(domain.ChainAuditLogs, 9, "h9")}
	res, _ := r.uc.VerifyChainIntegrity(context.Background(), uuid.New())
	if res.OK || res.Reason != domain.ReasonChainBroken || res.BrokenID == nil || *res.BrokenID != id {
		t.Fatalf("la causa de la rotura de filas se perdio: %+v", res)
	}
}

func TestLaRoturaDeLaCadenaDeEventosSubeAlVeredictoGeneral(t *testing.T) {
	r := newAnchorRig()
	id, seq, version := uuid.New(), int64(7), 2
	r.sec.verified = &domain.ChainIntegrity{OK: false, Chain: domain.ChainSecurityEvents, Reason: domain.ReasonHashKeyMissing,
		BrokenID: &id, BrokenSeq: &seq, BrokenVersion: &version}
	res, err := r.uc.VerifyChainIntegrity(context.Background(), uuid.New())
	if err != nil || res.OK {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Chain != domain.ChainSecurityEvents || res.Reason != domain.ReasonHashKeyMissing || *res.BrokenID != id || *res.BrokenSeq != 7 || *res.BrokenVersion != 2 {
		t.Fatalf("el veredicto general no dice cual cadena fallo ni por que: %+v", res)
	}
	if res.SecurityEvents == nil || res.SecurityEvents.OK {
		t.Fatalf("%+v", res.SecurityEvents)
	}
}

func TestVerificarPropagaLosFallosDeLectura(t *testing.T) {
	boom := errors.New("boom")
	for nombre, rig := range map[string]func(*anchorRig){
		"filas":   func(r *anchorRig) { r.logs.verifyErr = boom },
		"eventos": func(r *anchorRig) { r.sec.verifyErr = boom },
		"cabeza":  func(r *anchorRig) { r.anchors.headErr = boom },
		"anclas":  func(r *anchorRig) { r.anchors.findErr = boom },
	} {
		t.Run(nombre, func(t *testing.T) {
			r := newAnchorRig()
			rig(r)
			if _, err := r.uc.VerifyChainIntegrity(context.Background(), uuid.New()); !errors.Is(err, boom) {
				t.Fatalf("un fallo de lectura no puede volverse un veredicto: %v", err)
			}
		})
	}
}

func TestAnclarRegistraLaCabezaYLaPublicaEnLaMismaTransaccion(t *testing.T) {
	r := newAnchorRig()
	tenant := uuid.New()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 6, Hash: "h6", HashVersion: 2}
	results, err := r.uc.AnchorChains(context.Background(), tenant)
	if err != nil || len(results) != 2 {
		t.Fatalf("%+v %v", results, err)
	}
	if !results[0].Anchored || results[0].Chain != domain.ChainAuditLogs || results[0].HeadSeq != 6 {
		t.Fatalf("%+v", results[0])
	}
	if results[1].Anchored {
		t.Fatal("una cadena vacia no se ancla")
	}
	if len(r.anchors.saved) != 1 || len(r.events.anchored) != 1 || r.tx.calls != 1 {
		t.Fatalf("guardadas %d, publicadas %d, transacciones %d", len(r.anchors.saved), len(r.events.anchored), r.tx.calls)
	}
	a := r.events.anchored[0]
	if a.TenantID != tenant || a.HeadSeq != 6 || a.HeadHash != "h6" || a.HashVersion != 2 || a.AnchoredAt.IsZero() {
		t.Fatalf("%+v", a)
	}
}

func TestAnclarNoRepiteLaMismaCabeza(t *testing.T) {
	r := newAnchorRig()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 6, Hash: "h6"}
	r.anchors.found[domain.ChainAuditLogs] = domain.AnchorFindings{Last: anchorAt(domain.ChainAuditLogs, 6, "h6")}
	if _, err := r.uc.AnchorChains(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if len(r.anchors.saved) != 0 || len(r.events.anchored) != 0 {
		t.Fatal("se ancla otra vez una cabeza que ya esta anclada")
	}
}

// Anclar la cabeza de una cadena a la que le faltan filas ya ancladas la daria por buena: el
// hueco quedaria como la nueva referencia.
func TestAnclarNoAnclaUnaCadenaQueYaPerdioUnAncla(t *testing.T) {
	for nombre, found := range map[string]domain.AnchorFindings{
		"cabeza mas corta": {Last: anchorAt(domain.ChainAuditLogs, 9, "h9")},
		"hash distinto":    {Last: anchorAt(domain.ChainAuditLogs, 3, "h3"), Mismatched: anchorAt(domain.ChainAuditLogs, 3, "h3")},
	} {
		t.Run(nombre, func(t *testing.T) {
			r := newAnchorRig()
			r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 5, Hash: "h5"}
			r.anchors.found[domain.ChainAuditLogs] = found
			results, err := r.uc.AnchorChains(context.Background(), uuid.New())
			if err != nil || results[0].Reason == "" || results[0].Anchored {
				t.Fatalf("%+v %v", results, err)
			}
			if len(r.anchors.saved) != 0 || len(r.events.anchored) != 0 {
				t.Fatal("se ancla una cadena a la que le falta un ancla")
			}
		})
	}
}

func TestAnclarNoPublicaSiOtraReplicaYaAncloEsaPosicion(t *testing.T) {
	r := newAnchorRig()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 6, Hash: "h6"}
	r.anchors.alreadySaved = true
	results, err := r.uc.AnchorChains(context.Background(), uuid.New())
	if err != nil || results[0].Anchored || len(r.events.anchored) != 0 {
		t.Fatalf("%+v %v", results, err)
	}
}

func TestAnclarNoDaPorAnclaUnEventoQueNoSePudoEncolar(t *testing.T) {
	r := newAnchorRig()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 6, Hash: "h6"}
	r.events.err = errors.New("outbox caida")
	results, err := r.uc.AnchorChains(context.Background(), uuid.New())
	if err == nil || results[0].Anchored {
		t.Fatalf("%+v %v", results, err)
	}
}

func TestAnclarInformaElFalloYSeReintentaEnLaSiguientePasada(t *testing.T) {
	r := newAnchorRig()
	r.anchors.heads[domain.ChainAuditLogs] = &domain.ChainHead{Seq: 6, Hash: "h6"}
	r.anchors.heads[domain.ChainSecurityEvents] = &domain.ChainHead{Seq: 2, Hash: "e2"}
	r.anchors.saveErr = errors.New("sin permiso")
	results, err := r.uc.AnchorChains(context.Background(), uuid.New())
	if err == nil {
		t.Fatal("el fallo se perdio")
	}
	if len(results) != 0 {
		t.Fatalf("ninguna cadena pudo anclarse: %+v", results)
	}
	r.anchors.saveErr = nil
	if results, err = r.uc.AnchorChains(context.Background(), uuid.New()); err != nil || !results[0].Anchored || !results[1].Anchored {
		t.Fatalf("%+v %v", results, err)
	}
}
