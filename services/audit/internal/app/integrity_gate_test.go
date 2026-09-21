package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// slowLogs es un recorrido de cadena que no termina hasta que se le pide (o hasta que se cancela su
// contexto), como el de una cadena de millones de filas.
type slowLogs struct {
	fakeLogs
	started chan uuid.UUID
	release chan struct{}
}

func (s *slowLogs) VerifyChain(ctx context.Context, tenantID uuid.UUID, _ domain.VerifyOptions) (*domain.ChainIntegrity, error) {
	s.started <- tenantID
	select {
	case <-s.release:
		return &domain.ChainIntegrity{OK: true, Chain: domain.ChainAuditLogs}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newSlowRig() (*AuditUseCase, *slowLogs) {
	logs := &slowLogs{started: make(chan uuid.UUID, 16), release: make(chan struct{})}
	uc := NewAuditUseCase(AuditDeps{
		Logs: logs, Security: &fakeSecurity{}, Events: &fakePublisher{}, Logger: zap.NewNop(),
		Anchors: &fakeAnchors{heads: map[domain.ChainName]*domain.ChainHead{}, found: map[domain.ChainName]domain.AnchorFindings{}},
		Tx:      &passthroughTx{},
	})
	return uc, logs
}

func verifyAsync(uc *AuditUseCase, ctx context.Context, tenant uuid.UUID) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := uc.VerifyChainIntegrity(ctx, tenant)
		done <- err
	}()
	return done
}

func waitStarted(t *testing.T, logs *slowLogs) {
	t.Helper()
	select {
	case <-logs.started:
	case <-time.After(2 * time.Second):
		t.Fatal("la verificacion no arranco")
	}
}

// Recorrer la cadena de una empresa es una lectura de toda su tabla de auditoria, y la base de
// datos es compartida por todas las empresas: una empresa no puede lanzar el recorrido en bucle ni
// varios a la vez.
func TestUnaEmpresaVerificaSuCadenaDeUnaEnUna(t *testing.T) {
	uc, logs := newSlowRig()
	tenant := uuid.New()

	first := verifyAsync(uc, context.Background(), tenant)
	waitStarted(t, logs)

	if _, err := uc.VerifyChainIntegrity(context.Background(), tenant); !errors.Is(err, domain.ErrVerificationBusy) {
		t.Fatalf("segunda verificacion de la misma empresa: %v", err)
	}
	other := verifyAsync(uc, context.Background(), uuid.New())
	waitStarted(t, logs)

	close(logs.release)
	if err := <-first; err != nil {
		t.Fatalf("la primera verificacion: %v", err)
	}
	if err := <-other; err != nil {
		t.Fatalf("otra empresa no compite por el cupo de la primera: %v", err)
	}
	if _, err := uc.VerifyChainIntegrity(context.Background(), tenant); err != nil {
		t.Fatalf("al terminar, el cupo se libera: %v", err)
	}
}

func TestLasVerificacionesSimultaneasTienenUnTopeGlobal(t *testing.T) {
	uc, logs := newSlowRig()
	ctx, cancel := context.WithCancel(context.Background())
	var running []<-chan error
	for i := 0; i < maxConcurrentVerifications; i++ {
		running = append(running, verifyAsync(uc, ctx, uuid.New()))
		waitStarted(t, logs)
	}
	if _, err := uc.VerifyChainIntegrity(context.Background(), uuid.New()); !errors.Is(err, domain.ErrVerificationBusy) {
		t.Fatalf("pasado el tope: %v", err)
	}

	cancel()
	for _, done := range running {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("un recorrido cancelado debe terminar: %v", err)
		}
	}
	close(logs.release)
	if _, err := uc.VerifyChainIntegrity(context.Background(), uuid.New()); err != nil {
		t.Fatalf("cancelar libera los cupos: %v", err)
	}
}

func TestUnaVerificacionQueNoTerminaSeCorta(t *testing.T) {
	uc, logs := newSlowRig()
	uc.verifyTimeout = 20 * time.Millisecond
	tenant := uuid.New()
	done := verifyAsync(uc, context.Background(), tenant)
	waitStarted(t, logs)
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("la verificacion no se corto por tiempo")
	}
	close(logs.release)
	if _, err := uc.VerifyChainIntegrity(context.Background(), tenant); err != nil {
		t.Fatalf("tras el corte el cupo se libera: %v", err)
	}
}
