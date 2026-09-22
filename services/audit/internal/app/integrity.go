package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

var anchoredChains = []domain.ChainName{domain.ChainAuditLogs, domain.ChainSecurityEvents}

const (
	// maxConcurrentVerifications acota los recorridos de cadena a la vez en un proceso: cada uno lee
	// entera la tabla de auditoria de una empresa, en una base que comparten todas.
	maxConcurrentVerifications = 4
	// DefaultVerifyTimeout corta un recorrido que no termina; el contexto de la peticion ya lo
	// cancela si el cliente se va. Queda por debajo del WriteTimeout de pkg/server (30 s): pasado ese,
	// la respuesta ya no llegaria.
	DefaultVerifyTimeout = 25 * time.Second
)

// verificationGate deja un solo recorrido por empresa y un tope global, para que verificar la
// cadena no sea una forma de saturar la base de datos compartida.
type verificationGate struct {
	mu      sync.Mutex
	running map[uuid.UUID]struct{}
}

func (g *verificationGate) acquire(tenantID uuid.UUID) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, busy := g.running[tenantID]; busy || len(g.running) >= maxConcurrentVerifications {
		return nil, false
	}
	if g.running == nil {
		g.running = make(map[uuid.UUID]struct{}, maxConcurrentVerifications)
	}
	g.running[tenantID] = struct{}{}
	return func() {
		g.mu.Lock()
		delete(g.running, tenantID)
		g.mu.Unlock()
	}, true
}

// VerifyChainIntegrity verifica las filas de cada cadena y luego las contrasta con las anclas:
// una cadena cuyas filas enlazan bien pero cuya cabeza es anterior a un ancla publicada perdio
// sus ultimas filas. Devuelve domain.ErrVerificationBusy si la empresa ya tiene un recorrido en
// curso o el proceso esta en su tope.
func (uc *AuditUseCase) VerifyChainIntegrity(ctx context.Context, tenantID uuid.UUID) (*domain.ChainIntegrity, error) {
	release, ok := uc.verifying.acquire(tenantID)
	if !ok {
		return nil, domain.ErrVerificationBusy
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, uc.verifyTimeout)
	defer cancel()

	logs, err := uc.logs.VerifyChain(ctx, tenantID, domain.VerifyOptions{})
	if err != nil {
		return nil, err
	}
	events, err := uc.security.VerifyChain(ctx, tenantID, domain.VerifyOptions{})
	if err != nil {
		return nil, err
	}
	res, err := uc.conclude(ctx, logs, events)
	if err == nil && !res.OK {
		uc.noteChainBroken(ctx, tenantID, res.Chain, res.Reason)
	}
	return res, err
}

// noteChainBroken avisa fuera del servidor de una cadena rota, si hay a quien. El aviso no
// bloquea ni cambia el veredicto.
func (uc *AuditUseCase) noteChainBroken(ctx context.Context, tenantID uuid.UUID, chain domain.ChainName, reason string) {
	if uc.chainBreaks == nil {
		return
	}
	uc.chainBreaks.ChainBroken(ctx, tenantID, chain, reason)
}

// conclude contrasta cada cadena con sus anclas y da el veredicto del conjunto: el de las filas de
// audit_logs con el de security_events dentro, en rojo si cualquiera de las dos lo esta.
func (uc *AuditUseCase) conclude(ctx context.Context, logs, events *domain.ChainIntegrity) (*domain.ChainIntegrity, error) {
	if err := uc.checkAnchors(ctx, domain.ChainAuditLogs, logs); err != nil {
		return nil, err
	}
	if err := uc.checkAnchors(ctx, domain.ChainSecurityEvents, events); err != nil {
		return nil, err
	}
	logs.SecurityEvents = events
	if logs.OK && !events.OK {
		logs.OK, logs.Chain = false, events.Chain
		logs.Reason, logs.BrokenID, logs.BrokenSeq, logs.BrokenVersion = events.Reason, events.BrokenID, events.BrokenSeq, events.BrokenVersion
	}
	return logs, nil
}

func (uc *AuditUseCase) checkAnchors(ctx context.Context, chain domain.ChainName, res *domain.ChainIntegrity) error {
	head, err := uc.anchors.Head(ctx, chain)
	if err != nil {
		return err
	}
	found, err := uc.anchors.Findings(ctx, chain)
	if err != nil {
		return err
	}
	res.Anchor = found.Last
	if reason := domain.CheckAnchors(head, found); reason != "" && res.OK {
		res.OK, res.Reason = false, reason
	}
	return nil
}

// AnchorResult es lo que paso con una cadena en una pasada de anclaje.
type AnchorResult struct {
	Chain    domain.ChainName
	HeadSeq  int64
	Anchored bool
	// Reason no esta vacio si la cadena ya no contiene un ancla previa: no se ancla una
	// cabeza que contradice a las anteriores.
	Reason string
}

// AnchorChains registra la cabeza de cada cadena de la empresa y la publica como evento.
// Antes contrasta la cadena con las anclas que ya tiene: si le falta alguna no ancla nada y lo
// deja en el log con nivel de error, para que el borrado no quede como nueva referencia.
func (uc *AuditUseCase) AnchorChains(ctx context.Context, tenantID uuid.UUID) ([]AnchorResult, error) {
	var (
		results []AnchorResult
		errs    []error
	)
	for _, chain := range anchoredChains {
		r, err := uc.anchorChain(ctx, tenantID, chain)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", chain, err))
			continue
		}
		results = append(results, r)
	}
	return results, errors.Join(errs...)
}

func (uc *AuditUseCase) anchorChain(ctx context.Context, tenantID uuid.UUID, chain domain.ChainName) (AnchorResult, error) {
	res := AnchorResult{Chain: chain}
	head, err := uc.anchors.Head(ctx, chain)
	if err != nil || head == nil {
		return res, err
	}
	res.HeadSeq = head.Seq
	found, err := uc.anchors.Findings(ctx, chain)
	if err != nil {
		return res, err
	}
	if reason := domain.CheckAnchors(head, found); reason != "" {
		res.Reason = reason
		uc.logger.Error("audit: la cadena no contiene un ancla ya publicada",
			zap.String("tenant_id", tenantID.String()), zap.String("chain", string(chain)),
			zap.String("reason", reason), zap.Int64("head_seq", head.Seq))
		uc.noteChainBroken(ctx, tenantID, chain, reason)
		return res, nil
	}
	if found.Last != nil && found.Last.HeadSeq == head.Seq {
		return res, nil
	}
	anchor := &domain.ChainAnchor{TenantID: tenantID, Chain: chain, HeadSeq: head.Seq, HeadHash: head.Hash, HashVersion: head.HashVersion}
	err = uc.tx.Transact(ctx, func(ctx context.Context) error {
		inserted, err := uc.anchors.Save(ctx, anchor)
		if err != nil || !inserted {
			return err
		}
		res.Anchored = true
		return uc.anchorEvents.ChainAnchored(ctx, anchor)
	})
	if err != nil {
		res.Anchored = false
		return res, err
	}
	if res.Anchored {
		uc.logger.Info("audit: cabeza de cadena anclada",
			zap.String("tenant_id", tenantID.String()), zap.String("chain", string(chain)),
			zap.Int64("head_seq", head.Seq), zap.String("head_hash", head.Hash), zap.Int("hash_version", head.HashVersion))
	}
	return res, nil
}
