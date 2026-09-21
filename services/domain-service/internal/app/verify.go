package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// VerifyResult es la respuesta de una verificacion: el dominio con su estado nuevo, el
// detalle por registro y el veredicto.
type VerifyResult struct {
	Domain  *domain.Domain
	Checks  []domain.DNSCheck
	Outcome domain.Outcome
	// IntegrationErrors: fallos al activar o publicar claves tras verificar. El estado
	// ya es verified; el barrido lo reintenta.
	IntegrationErrors []string
}

// Verify consulta el DNS y aplica el resultado al dominio. Es una consulta: no falla por
// que falten registros, lo cuenta.
func (uc *UseCase) Verify(ctx context.Context, tenantID, id uuid.UUID) (*VerifyResult, error) {
	d, err := uc.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	return uc.verify(ctx, d, false)
}

// verify es el nucleo compartido por la verificacion manual y el barrido. sweep cambia
// dos cosas: un pendiente que no verifica se queda en pending (nadie lo pidio), y un
// verificado solo cae a failed si pierde la propiedad, el MX o el SPF, nunca por el
// DKIM, que durante una rotacion puede tardar en publicarse. A mano un verificado tampoco cae
// solo por el DKIM mientras el TXT de su clave actual no se haya visto nunca: la cambio la
// plataforma (rotacion o revocacion) y apagarle el correo no retira ninguna clave.
func (uc *UseCase) verify(ctx context.Context, d *domain.Domain, sweep bool) (*VerifyResult, error) {
	now := uc.now()
	expected := uc.ExpectedRecords(ctx, d)
	result := domain.Evaluate(d, expected, uc.observe(ctx, expected), now)
	if err := uc.repo.SaveChecks(ctx, result.Checks); err != nil {
		return nil, fmt.Errorf("guardar comprobaciones DNS: %w", err)
	}
	d.LastCheckedAt = &now

	previous, wasActive := d.Status, d.ActiveInDirectory()
	keyConfirmed := d.DKIMConfirmedAt != nil
	switch result.Outcome {
	case domain.OutcomeVerified:
		if d.Status != domain.StatusVerified {
			d.Status = domain.StatusVerified
			d.VerifiedAt = &now
		}
	case domain.OutcomeFailed:
		switch {
		case sweep && d.Status == domain.StatusPending:
			// Sigue esperando a que el cliente publique; no es un fallo suyo.
		case d.Status == domain.StatusVerified && !lostRoutingRecord(result.Checks) && (sweep || !keyConfirmed):
			// Solo el DKIM falla: se registra, pero el dominio sigue recibiendo.
		default:
			d.Status = domain.StatusFailed
			d.VerifiedAt = nil
		}
	case domain.OutcomeInconclusive:
		// El DNS no respondio: no se sabe nada nuevo y el estado no cambia.
	}
	switch {
	case d.ActiveInDirectory():
		// syncVerified lo activa: una desactivacion pendiente ya no toca.
		d.DirectoryDeactivationPending = false
	case wasActive:
		// Deja de recibir: la marca se guarda antes de llamar, para que el barrido termine la
		// desactivacion si esta llamada no llega.
		d.DirectoryDeactivationPending = true
	}
	if err := uc.repo.Update(ctx, d); err != nil {
		return nil, err
	}
	if err := uc.recordDKIMSigning(ctx, d, result, keyConfirmed, now); err != nil {
		return nil, err
	}

	out := &VerifyResult{Domain: d, Checks: result.Checks, Outcome: result.Outcome}
	switch {
	case d.Status == domain.StatusVerified:
		out.IntegrationErrors = uc.syncVerified(ctx, d, result.SignWithPrevious)
		if previous != domain.StatusVerified {
			uc.publish("domains.domain.verified", d, func() error { return uc.events.DomainVerified(ctx, d) })
		}
	case d.Status == domain.StatusFailed && previous == domain.StatusVerified:
		if wasActive {
			if err := uc.completeDeactivation(ctx, d); err != nil {
				uc.logger.Error("no se pudo desactivar el dominio en mail-directory; se reintenta en el barrido",
					zap.String("domain", d.Domain), zap.String("tenant_id", d.TenantID.String()), zap.Error(err))
				out.IntegrationErrors = append(out.IntegrationErrors, "desactivar en mail-directory: "+err.Error())
			}
		}
		uc.publish("domains.domain.failed", d, func() error { return uc.events.DomainFailed(ctx, d) })
	case d.Status == domain.StatusFailed && previous != domain.StatusFailed:
		uc.publish("domains.domain.failed", d, func() error { return uc.events.DomainFailed(ctx, d) })
	}
	return out, nil
}

// lostRoutingRecord dice si fallo, con respuesta del DNS, alguno de los registros que
// hacen que el correo llegue o salga por la celda: propiedad, MX o SPF.
func lostRoutingRecord(checks []domain.DNSCheck) bool {
	for _, c := range checks {
		switch c.Record {
		case domain.RecordOwnershipTXT, domain.RecordMX, domain.RecordSPF:
			if !c.OK {
				return true
			}
		}
	}
	return false
}

// syncVerified aplica lo que un dominio verificado debe tener fuera de esta base: reclamado en
// el indice global de dominios y activo en el directorio de la celda si recibe correo, y sus
// claves DKIM en los motores. Todas las llamadas son idempotentes y se repiten en cada barrido,
// que es lo que las cura si aqui fallan (tambien si no llegan a la celda de la empresa: el
// estado sale solo del DNS) y lo que hace converger el indice.
func (uc *UseCase) syncVerified(ctx context.Context, d *domain.Domain, signWithPrevious bool) []string {
	var failures []string
	if d.Purpose.IncludesCorporate() {
		if err := uc.activateInDirectory(ctx, d); err != nil {
			if errors.Is(err, domain.ErrDomainClaimedElsewhere) || errors.Is(err, domain.ErrTenantBeingRemoved) {
				uc.logger.Warn("el dominio no se activa en el directorio de la celda",
					zap.String("domain", d.Domain), zap.String("tenant_id", d.TenantID.String()), zap.Error(err))
				// La celda no sirve el dominio: sus claves DKIM no van a los motores, que las rechazarian.
				return append(failures, "activar en mail-directory: "+err.Error())
			} else {
				uc.logger.Error("no se pudo activar el dominio en mail-directory; se reintenta en el barrido",
					zap.String("domain", d.Domain), zap.String("tenant_id", d.TenantID.String()), zap.Error(err))
			}
			failures = append(failures, "activar en mail-directory: "+err.Error())
		}
	}
	if err := uc.publishDKIMFor(ctx, d, signWithPrevious); err != nil {
		uc.logger.Error("no se pudieron publicar las claves DKIM; se reintenta en el barrido",
			zap.String("domain", d.Domain), zap.String("tenant_id", d.TenantID.String()), zap.Error(err))
		failures = append(failures, "publicar DKIM en mail-security: "+err.Error())
	}
	return failures
}

// completeDeactivation desactiva en el directorio de la celda un dominio que ya no debe recibir,
// lo suelta del indice global de dominios y, confirmado, quita la marca. Si no se confirma, la
// marca queda y el barrido lo repite.
func (uc *UseCase) completeDeactivation(ctx context.Context, d *domain.Domain) error {
	if err := uc.retireFromDirectory(ctx, d); err != nil {
		return err
	}
	d.DirectoryDeactivationPending = false
	return uc.repo.Update(ctx, d)
}

// observe consulta cada registro esperado. Un error de consulta se conserva en la
// observacion: Evaluate lo distingue de un registro ausente.
func (uc *UseCase) observe(ctx context.Context, expected []domain.DNSRecord) map[domain.RecordKind]domain.Observation {
	observed := make(map[domain.RecordKind]domain.Observation, len(expected))
	for _, rec := range expected {
		var obs domain.Observation
		switch rec.Type {
		case "MX":
			obs.MX, obs.Err = uc.dns.LookupMX(ctx, rec.Host)
		default:
			obs.TXT, obs.Err = uc.dns.LookupTXT(ctx, rec.Host)
		}
		if obs.Err != nil && errors.Is(obs.Err, context.Canceled) {
			obs.Err = fmt.Errorf("consulta cancelada")
		}
		observed[rec.Record] = obs
	}
	return observed
}
