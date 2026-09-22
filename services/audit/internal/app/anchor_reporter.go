package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// chainBreakNoticeEvery es el minimo entre dos envios inmediatos por la misma empresa: una
	// cadena rota se re-verifica en cada pasada del barrido y en cada GET /integrity, y un correo
	// por cada una no anade evidencia. El informe periodico la sigue llevando.
	chainBreakNoticeEvery = time.Hour
	// chainBreakNoticeTimeout acota el envio inmediato, que corre fuera de la peticion o del
	// barrido que lo provoco.
	chainBreakNoticeTimeout = 2 * time.Minute
)

// AnchorReporter saca la cabeza de las cadenas fuera del servidor (docs/adr/0006, seccion 8):
// compone el informe con la ultima ancla de cada cadena de cada empresa, lo firma con la llave de
// la cadena si la hay y lo envia como correo de la plataforma a las direcciones externas. Tambien
// avisa de inmediato de una cadena que una verificacion dio por rota.
type AnchorReporter struct {
	anchors    ports.ChainAnchorRepository
	directory  ports.TenantDirectory
	sender     ports.AnchorReportSender
	signer     ports.ReportSigner
	metrics    ports.AnchorReportMetrics
	recipients []string
	logger     *zap.Logger
	now        func() time.Time

	mu        sync.Mutex
	lastBreak map[uuid.UUID]time.Time
	inflight  sync.WaitGroup
}

type AnchorReporterDeps struct {
	Anchors   ports.ChainAnchorRepository
	Directory ports.TenantDirectory
	Sender    ports.AnchorReportSender
	// Signer es nil si el servicio no tiene AUDIT_HASH_KEY: el informe sale sin firma y lo dice.
	Signer     ports.ReportSigner
	Metrics    ports.AnchorReportMetrics
	Recipients []string
	Logger     *zap.Logger
}

func NewAnchorReporter(d AnchorReporterDeps) *AnchorReporter {
	return &AnchorReporter{
		anchors: d.Anchors, directory: d.Directory, sender: d.Sender, signer: d.Signer, metrics: d.Metrics,
		recipients: d.Recipients, logger: d.Logger, now: time.Now, lastBreak: map[uuid.UUID]time.Time{},
	}
}

// Collect lee la ultima ancla de cada cadena de la empresa. El contexto lleva su base.
func (r *AnchorReporter) Collect(ctx context.Context, tenant domain.TenantRef) (domain.TenantAnchors, error) {
	out := domain.TenantAnchors{Tenant: tenant}
	for _, chain := range anchoredChains {
		found, err := r.anchors.Findings(ctx, chain)
		if err != nil {
			return out, fmt.Errorf("%s: %w", chain, err)
		}
		if found.Last != nil {
			out.Anchors = append(out.Anchors, *found.Last)
		}
	}
	return out, nil
}

// SendScheduled envia el informe periodico con lo recogido de todas las empresas.
func (r *AnchorReporter) SendScheduled(ctx context.Context, tenants []domain.TenantAnchors) error {
	return r.send(ctx, domain.AnchorReport{GeneratedAt: r.now(), Cause: domain.ReportCauseScheduled, Tenants: tenants})
}

// send firma y entrega el informe a cada direccion. Cada envio cuenta en la metrica; el informe
// se da por salido del servidor si al menos una direccion lo acepto. El error devuelto es el de
// todas las direcciones cuando ninguna lo recibio.
func (r *AnchorReporter) send(ctx context.Context, report domain.AnchorReport) error {
	var sig *domain.ReportSignature
	if r.signer != nil {
		sig = &domain.ReportSignature{KeyID: r.signer.KeyID(), MAC: r.signer.Sign(report.Block())}
	}
	subject, body := report.Subject(), report.Render(sig)
	delivered := 0
	var errs []error
	for _, to := range r.recipients {
		result, err := r.deliver(ctx, to, subject, body)
		r.metrics.ReportResult(result)
		if err == nil {
			delivered++
			continue
		}
		errs = append(errs, err)
		r.logger.Warn("audit: informe de anclas no entregado a una direccion",
			zap.String("cause", string(report.Cause)), zap.String("result", result), zap.Error(err))
	}
	if delivered == 0 {
		return errors.Join(errs...)
	}
	r.metrics.ReportSucceeded(r.now())
	r.logger.Info("audit: informe de anclas enviado", zap.String("cause", string(report.Cause)),
		zap.Int("tenants", len(report.Tenants)), zap.Int("delivered", delivered), zap.Int("undelivered", len(errs)),
		zap.Bool("signed", sig != nil))
	return nil
}

func (r *AnchorReporter) deliver(ctx context.Context, to, subject, body string) (string, error) {
	suppressed, err := r.sender.SendAnchorReport(ctx, to, subject, body)
	var rejected *ports.ReportRejectedError
	switch {
	case errors.As(err, &rejected):
		return domain.ReportResultRejected, err
	case err != nil:
		return domain.ReportResultFailed, err
	case suppressed:
		return domain.ReportResultSuppressed, errors.New("la direccion esta suprimida en transactional; el informe no salio")
	}
	return domain.ReportResultSent, nil
}

// ChainBroken envia el informe de esa empresa sin esperar al periodico, fuera del hilo del
// llamador y como mucho una vez por hora y empresa. El contexto solo aporta la base de la empresa:
// su cancelacion (una peticion que termina, un barrido que agota su plazo) no corta el envio.
func (r *AnchorReporter) ChainBroken(ctx context.Context, tenantID uuid.UUID, chain domain.ChainName, reason string) {
	if !r.allowBreakNotice(tenantID) {
		return
	}
	r.inflight.Add(1)
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), chainBreakNoticeTimeout)
	go func() {
		defer r.inflight.Done()
		defer cancel()
		r.notifyBreak(detached, domain.ChainBreak{TenantID: tenantID, Chain: chain, Reason: reason})
	}()
}

func (r *AnchorReporter) allowBreakNotice(tenantID uuid.UUID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if last, ok := r.lastBreak[tenantID]; ok && now.Sub(last) < chainBreakNoticeEvery {
		return false
	}
	r.lastBreak[tenantID] = now
	return true
}

func (r *AnchorReporter) notifyBreak(ctx context.Context, brk domain.ChainBreak) {
	log := r.logger.With(zap.String("tenant_id", brk.TenantID.String()), zap.String("chain", string(brk.Chain)), zap.String("reason", brk.Reason))
	tenant := domain.TenantRef{ID: brk.TenantID}
	if slug, err := r.slugOf(ctx, brk.TenantID); err != nil {
		log.Warn("audit: el aviso de cadena rota sale sin el slug de la empresa", zap.Error(err))
	} else {
		tenant.Slug = slug
	}
	collected, err := r.Collect(ctx, tenant)
	if err != nil {
		log.Error("audit: el aviso de cadena rota no pudo leer las anclas; el informe periodico las llevara", zap.Error(err))
		return
	}
	report := domain.AnchorReport{GeneratedAt: r.now(), Cause: domain.ReportCauseChainBroken, Broken: &brk, Tenants: []domain.TenantAnchors{collected}}
	if err := r.send(ctx, report); err != nil {
		log.Error("audit: el aviso de cadena rota no salio del servidor", zap.Error(err))
	}
}

func (r *AnchorReporter) slugOf(ctx context.Context, tenantID uuid.UUID) (string, error) {
	tenants, err := r.directory.ActiveTenants(ctx)
	if err != nil {
		return "", err
	}
	for _, t := range tenants {
		if t.ID == tenantID {
			return t.Slug, nil
		}
	}
	return "", nil
}
