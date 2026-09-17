package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// selectorPrefix identifica las claves emitidas por la plataforma en la zona del
// cliente: cfm202609, cfm20260912, ...
const selectorPrefix = "cfm"

// dkimHistoryLimit acota las entradas del historial de claves que devuelve la ficha del dominio.
const dkimHistoryLimit = 20

// dkimSelector devuelve el selector del mes; si coincide con alguno en uso (dos
// rotaciones en el mismo mes) afina a dia y luego a segundo, y con mas de una clave en el
// mismo segundo le anade un sufijo. Nunca repite uno en uso: un selector repetido pisaria el
// TXT del anterior en la zona del cliente, y el de una clave revocada seguiria validandola.
func dkimSelector(now time.Time, inUse ...string) string {
	now = now.UTC()
	taken := make(map[string]bool, len(inUse))
	for _, s := range inUse {
		taken[s] = true
	}
	for _, layout := range []string{"200601", "20060102", "20060102150405"} {
		if candidate := selectorPrefix + now.Format(layout); !taken[candidate] {
			return candidate
		}
	}
	base := selectorPrefix + now.Format("20060102150405")
	for n := 2; ; n++ {
		if candidate := fmt.Sprintf("%s-%d", base, n); !taken[candidate] {
			return candidate
		}
	}
}

// dkimKeyPair es un par recien generado: el PEM privado solo vive en memoria.
type dkimKeyPair struct {
	privatePEM string
	publicB64  string
}

func generateDKIMKeyPair() (dkimKeyPair, error) {
	priv, err := rsa.GenerateKey(rand.Reader, domain.DKIMKeyBits)
	if err != nil {
		return dkimKeyPair{}, fmt.Errorf("generar clave DKIM: %w", err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return dkimKeyPair{}, fmt.Errorf("serializar clave publica DKIM: %w", err)
	}
	// PKCS#1 en PEM es el formato que Rspamd lee de DKIM_PRIV_KEYS.
	block := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}
	return dkimKeyPair{
		privatePEM: string(pem.EncodeToMemory(block)),
		publicB64:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}

// assignDKIMKey genera un par nuevo y lo deja cifrado en el dominio como clave actual, todavia
// sin ver publicado su TXT.
func (uc *UseCase) assignDKIMKey(d *domain.Domain, selector string) error {
	pair, err := generateDKIMKeyPair()
	if err != nil {
		return err
	}
	enc, err := uc.cipher.Encrypt([]byte(pair.privatePEM))
	if err != nil {
		return fmt.Errorf("cifrar clave DKIM: %w", err)
	}
	d.DKIMSelector = selector
	d.DKIMPrivateKeyEnc = enc
	d.DKIMPublicKey = pair.publicB64
	d.DKIMKeyBits = domain.DKIMKeyBits
	d.DKIMConfirmedAt = nil
	return nil
}

// newSelector elige el selector de una clave nueva sin repetir ninguno que el dominio haya usado:
// el TXT de una clave revocada puede seguir en la zona del cliente o en la cache de un receptor.
func (uc *UseCase) newSelector(ctx context.Context, d *domain.Domain, now time.Time) (string, error) {
	used, err := uc.repo.UsedDKIMSelectors(ctx, d.TenantID, d.ID)
	if err != nil {
		return "", fmt.Errorf("selectores DKIM usados: %w", err)
	}
	return dkimSelector(now, append(used, d.DKIMSelectors()...)...), nil
}

// signingKeys descifra las claves que hay que entregar a mail-security, en el orden en
// que deben publicarse: la ultima es la que firma. Con signWithPrevious la anterior va
// al final porque el TXT de la nueva aun no esta en la zona del cliente.
func (uc *UseCase) signingKeys(d *domain.Domain, signWithPrevious bool) ([]ports.DKIMKey, error) {
	current, err := uc.cipher.Decrypt(d.DKIMPrivateKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("descifrar clave DKIM de %s: %w", d.Domain, err)
	}
	keys := []ports.DKIMKey{{Selector: d.DKIMSelector, PrivateKeyPEM: string(current)}}
	if !d.HasPreviousDKIM() {
		return keys, nil
	}
	previous, err := uc.cipher.Decrypt(d.DKIMPreviousPrivateKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("descifrar clave DKIM anterior de %s: %w", d.Domain, err)
	}
	prev := ports.DKIMKey{Selector: d.DKIMPreviousSelector, PrivateKeyPEM: string(previous)}
	if signWithPrevious {
		return append(keys, prev), nil
	}
	return append([]ports.DKIMKey{prev}, keys...), nil
}

// currentDKIMRecord es el TXT de la clave actual que el cliente debe publicar.
func currentDKIMRecord(d *domain.Domain) domain.DNSRecord {
	return domain.DNSRecord{
		Record: domain.RecordDKIM, Type: "TXT", Required: true,
		Host: domain.DKIMHost(d.DKIMSelector, d.Domain), Value: domain.DKIMValue(d.DKIMPublicKey),
	}
}

// RotateDKIMResult es la respuesta de una rotacion: el TXT nuevo que hay que publicar y
// hasta cuando se conserva, como pronto, el selector anterior.
type RotateDKIMResult struct {
	Domain     *domain.Domain
	Record     domain.DNSRecord
	GraceUntil time.Time
	// DNS es lo que publico la plataforma en el proveedor si el dominio publica en automatico.
	DNS *DNSAutomationResult
}

// RotateDKIM es la rotacion programada: genera un par nuevo y conserva el anterior durante la
// gracia. El firmado no cambia hasta que una verificacion vea publicado el TXT del selector
// nuevo, y la gracia se cuenta desde la ultima vez que la clave anterior pudo firmar, porque lo
// que firmo puede seguir en la cola de Postfix. Con otra clave aun en gracia se niega
// (domain.ErrDKIMRotationInProgress): retirarla antes romperia el DKIM de ese correo; una clave
// comprometida se retira con RevokeDKIM.
func (uc *UseCase) RotateDKIM(ctx context.Context, tenantID, id, actorID uuid.UUID) (*RotateDKIMResult, error) {
	var out *RotateDKIMResult
	err := uc.repo.WithDKIMLock(ctx, tenantID, id, func(ctx context.Context, d *domain.Domain) error {
		if d.HasPreviousDKIM() {
			return domain.ErrDKIMRotationInProgress
		}
		now := uc.now()
		selector, err := uc.newSelector(ctx, d, now)
		if err != nil {
			return err
		}
		old := *d
		if err := uc.assignDKIMKey(d, selector); err != nil {
			return err
		}
		d.DKIMPreviousSelector = old.DKIMSelector
		d.DKIMPreviousPrivateKeyEnc = old.DKIMPrivateKeyEnc
		d.DKIMPreviousPublicKey = old.DKIMPublicKey
		d.DKIMRotatedAt, d.DKIMPreviousSignedAt = &now, &now
		rotation := &domain.DKIMRotation{
			ID: uuid.New(), TenantID: tenantID, DomainID: id, Kind: domain.RotationScheduled,
			Selector: d.DKIMSelector, PreviousSelector: old.DKIMSelector, ActorID: actorID, RotatedAt: now,
		}
		if err := uc.repo.SaveDKIMKeys(ctx, d, old.DKIMSelector, rotation); err != nil {
			return err
		}
		if err := uc.keyEvents.DKIMRotated(ctx, d, rotation); err != nil {
			return fmt.Errorf("encolar domains.domain.dkim_rotated: %w", err)
		}
		// El TXT nuevo no esta publicado todavia: la clave nueva se deposita pero se sigue
		// firmando con la anterior. Si la celda no responde, el barrido lo repite.
		if err := uc.publishDKIM(ctx, d, true); err != nil {
			uc.logger.Warn("no se pudieron publicar las claves DKIM; se reintenta en el barrido",
				zap.String("domain", d.Domain), zap.Error(err))
		}
		out = &RotateDKIMResult{Domain: d, Record: currentDKIMRecord(d), GraceUntil: d.PreviousDKIMRetireAfter(uc.rotationGrace)}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.DNS = uc.publishDKIMAutomatically(ctx, tenantID, id, nil)
	return out, nil
}

// RevokeDKIMRequest pide revocar las claves de un dominio por compromiso. CurrentSelector es el
// selector actual que vio quien la pide: hace de condicion previa y de clave de idempotencia.
type RevokeDKIMRequest struct {
	CurrentSelector string
	Reason          string
	ActorID         uuid.UUID
}

// RevokeDKIMResult es la respuesta de una revocacion: el TXT nuevo que hay que publicar, los TXT
// que el cliente debe retirar de su DNS ya (la plataforma no puede tocar su zona) y si la celda
// confirmo que sus motores ya no tienen ninguna clave revocada.
type RevokeDKIMResult struct {
	Domain            *domain.Domain
	Rotation          domain.DKIMRotation
	Record            domain.DNSRecord
	RemoveRecords     []domain.DNSRecord
	EnginesRetired    bool
	IntegrationErrors []string
	// DNS es lo que publico y retiro la plataforma en el proveedor si el dominio publica en
	// automatico: el TXT nuevo y los TXT revocados que ella misma habia publicado.
	DNS *DNSAutomationResult
}

// RevokeDKIM retira de inmediato todas las claves del dominio (la actual y la que siga en gracia)
// y firma con una nueva. Primero guarda en una transaccion la clave nueva, la marca de
// revocacion pendiente, el historial con motivo y actor y el evento; despues entrega a la celda
// solo la clave nueva: mail-security deja firmando la nueva antes de borrar las demas, asi que el
// dominio nunca se queda sin clave en la celda. Nada que se publique despues puede devolver una
// clave revocada a los motores, porque la fila ya no la tiene.
//
// Repetirla con el mismo CurrentSelector (un reintento tras un corte) no genera otra clave:
// devuelve la revocacion que ya se hizo y reintenta lo que falte en la celda.
func (uc *UseCase) RevokeDKIM(ctx context.Context, tenantID, id uuid.UUID, req RevokeDKIMRequest) (*RevokeDKIMResult, error) {
	reason, err := domain.NormalizeRevocationReason(req.Reason)
	if err != nil {
		return nil, err
	}
	selector := strings.ToLower(strings.TrimSpace(req.CurrentSelector))
	var rotation domain.DKIMRotation
	err = uc.repo.WithDKIMLock(ctx, tenantID, id, func(ctx context.Context, d *domain.Domain) error {
		if selector != d.DKIMSelector {
			last, err := uc.lastRotation(ctx, d)
			if err != nil {
				return err
			}
			if !last.Revoked(selector) || last.Selector != d.DKIMSelector {
				return domain.ErrDKIMSelectorNotCurrent
			}
			rotation = *last
			return nil
		}
		now := uc.now()
		newSelector, err := uc.newSelector(ctx, d, now)
		if err != nil {
			return err
		}
		revoked := d.DKIMSelectors()
		pending := d.DKIMRevocationPending || d.KeysMayBeInCell()
		if err := uc.assignDKIMKey(d, newSelector); err != nil {
			return err
		}
		d.ClearPreviousDKIM()
		d.DKIMRevocationPending = pending
		rotation = domain.DKIMRotation{
			ID: uuid.New(), TenantID: tenantID, DomainID: id, Kind: domain.RotationCompromised,
			Selector: d.DKIMSelector, RevokedSelectors: revoked, Reason: reason, ActorID: req.ActorID, RotatedAt: now,
		}
		if err := uc.repo.SaveDKIMKeys(ctx, d, selector, &rotation); err != nil {
			return err
		}
		if err := uc.keyEvents.DKIMRevoked(ctx, d, &rotation); err != nil {
			return fmt.Errorf("encolar domains.domain.dkim_revoked: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	res := &RevokeDKIMResult{Rotation: rotation, EnginesRetired: true}
	res.DNS = uc.publishDKIMAutomatically(ctx, tenantID, id, rotation.RevokedSelectors)
	if err := uc.finishRevocation(ctx, tenantID, id); err != nil {
		uc.logger.Error("revocacion DKIM guardada pero sin confirmar en la celda: la clave revocada puede seguir firmando; se reintenta en el barrido",
			zap.String("tenant_id", tenantID.String()), zap.String("domain_id", id.String()), zap.Error(err))
		res.IntegrationErrors = append(res.IntegrationErrors, "retirar las claves revocadas en mail-security: "+err.Error())
	}
	d, err := uc.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	res.Domain = d
	res.EnginesRetired = !d.DKIMRevocationPending
	res.Record = currentDKIMRecord(d)
	for _, s := range rotation.RevokedSelectors {
		res.RemoveRecords = append(res.RemoveRecords, domain.DNSRecord{
			Record: domain.RecordDKIM, Type: "TXT", Host: domain.DKIMHost(s, d.Domain),
		})
	}
	return res, nil
}

// lastRotation devuelve la entrada mas reciente del historial del dominio, o nil.
func (uc *UseCase) lastRotation(ctx context.Context, d *domain.Domain) (*domain.DKIMRotation, error) {
	rotations, err := uc.repo.ListDKIMRotations(ctx, d.TenantID, d.ID, 1)
	if err != nil {
		return nil, fmt.Errorf("historial DKIM: %w", err)
	}
	if len(rotations) == 0 {
		return nil, nil
	}
	return &rotations[0], nil
}

// DKIMRotations devuelve el historial de claves del dominio, la mas reciente primero.
func (uc *UseCase) DKIMRotations(ctx context.Context, tenantID, id uuid.UUID) ([]domain.DKIMRotation, error) {
	return uc.repo.ListDKIMRotations(ctx, tenantID, id, dkimHistoryLimit)
}

// PreviousDKIMRetireAfter es hasta cuando, como pronto, debe seguir publicado el TXT de la clave
// anterior; nil si no hay clave anterior.
func (uc *UseCase) PreviousDKIMRetireAfter(d *domain.Domain) *time.Time {
	if !d.HasPreviousDKIM() {
		return nil
	}
	t := d.PreviousDKIMRetireAfter(uc.rotationGrace)
	return &t
}

// finishRevocation completa en la celda una revocacion pendiente, con el cerrojo del dominio y su
// fila al dia. Un dominio activo en el directorio recibe su juego actual completo, que retira
// cualquier otro selector; uno que la celda ya no debe servir pierde todas sus claves alli. Solo
// una respuesta de la celda quita la marca.
func (uc *UseCase) finishRevocation(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.repo.WithDKIMLock(ctx, tenantID, id, func(ctx context.Context, d *domain.Domain) error {
		if !d.DKIMRevocationPending {
			return nil
		}
		if d.ActiveInDirectory() {
			return uc.pushKeys(ctx, d, d.HasPreviousDKIM() && d.DKIMConfirmedAt == nil)
		}
		if err := uc.mailSecurity.DeleteDKIM(ctx, d.TenantID, d.Domain); err != nil {
			return err
		}
		return uc.repo.CompleteDKIMRevocation(ctx, d.TenantID, d.ID, d.DKIMSelector)
	})
}

// publishDKIM entrega a mail-security el juego completo de claves de un dominio activo en el
// directorio de su celda. Uno que la celda no sirve (solo de envio) no lleva claves a los
// motores: mail-security las rechazaria y ningun correo de la celda se firmaria con ellas.
// Debe llamarse con el cerrojo del dominio (WithDKIMLock) y la fila leida dentro de el.
func (uc *UseCase) publishDKIM(ctx context.Context, d *domain.Domain, signWithPrevious bool) error {
	if !d.ActiveInDirectory() {
		return nil
	}
	return uc.pushKeys(ctx, d, signWithPrevious)
}

// pushKeys hace el PUT del juego completo. Como mail-security retira todo selector que el juego no
// trae, una entrega confirmada completa tambien una revocacion pendiente.
func (uc *UseCase) pushKeys(ctx context.Context, d *domain.Domain, signWithPrevious bool) error {
	keys, err := uc.signingKeys(d, signWithPrevious)
	if err != nil {
		return err
	}
	if err := uc.mailSecurity.PublishDKIM(ctx, d.TenantID, d.Domain, keys); err != nil {
		return err
	}
	if d.DKIMRevocationPending {
		if err := uc.repo.CompleteDKIMRevocation(ctx, d.TenantID, d.ID, d.DKIMSelector); err != nil {
			return err
		}
		d.DKIMRevocationPending = false
	}
	return nil
}

// publishDKIMFor publica las claves de un dominio que se leyo fuera del cerrojo (verificacion,
// barrido). Vuelve a leerlo con el cerrojo tomado y, si una rotacion o una revocacion cambio sus
// claves entretanto, no publica: esa operacion ya entrego su juego, y el orden decidido con las
// claves de antes podria devolver a los motores una clave revocada.
func (uc *UseCase) publishDKIMFor(ctx context.Context, seen *domain.Domain, signWithPrevious bool) error {
	return uc.repo.WithDKIMLock(ctx, seen.TenantID, seen.ID, func(ctx context.Context, d *domain.Domain) error {
		if d.DKIMSelector != seen.DKIMSelector || d.DKIMPreviousSelector != seen.DKIMPreviousSelector {
			return nil
		}
		return uc.publishDKIM(ctx, d, signWithPrevious)
	})
}

// retirePreviousDKIM saca de gracia la clave anterior: la retira de mail-security si pudo llegar
// a los motores y la olvida, con el cerrojo del dominio. Si mail-security no responde, se deja
// para el siguiente barrido; si otra operacion ya la retiro, no hay nada que hacer.
func (uc *UseCase) retirePreviousDKIM(ctx context.Context, seen *domain.Domain) error {
	selector := seen.DKIMPreviousSelector
	return uc.repo.WithDKIMLock(ctx, seen.TenantID, seen.ID, func(ctx context.Context, d *domain.Domain) error {
		if d.DKIMPreviousSelector != selector {
			return nil
		}
		if d.KeysMayBeInCell() {
			if err := uc.mailSecurity.RetireDKIM(ctx, d.TenantID, d.Domain, selector); err != nil {
				return fmt.Errorf("retirar selector %s en mail-security: %w", selector, err)
			}
		}
		return uc.repo.ClearPreviousDKIM(ctx, d.TenantID, d.ID, selector)
	})
}

// recordDKIMSigning anota lo que una verificacion supo de las claves: mientras el TXT de la clave
// actual no se haya visto nunca, o mientras se siga firmando con la anterior, la anterior pudo
// firmar hasta ahora; y la primera vez que se ve publicado el TXT de la actual. Las dos escrituras
// van condicionadas al selector comprobado: si una rotacion lo cambio entretanto, no tocan nada.
func (uc *UseCase) recordDKIMSigning(ctx context.Context, d *domain.Domain, result domain.VerificationResult, keyConfirmed bool, now time.Time) error {
	if d.HasPreviousDKIM() && (result.SignWithPrevious || !keyConfirmed) {
		if err := uc.repo.MarkPreviousDKIMSigning(ctx, d.TenantID, d.ID, d.DKIMPreviousSelector, now); err != nil {
			return fmt.Errorf("anotar la firma con la clave DKIM anterior: %w", err)
		}
		d.DKIMPreviousSignedAt = &now
	}
	if !keyConfirmed && recordOK(result.Checks, domain.RecordDKIM) {
		if err := uc.repo.ConfirmDKIM(ctx, d.TenantID, d.ID, d.DKIMSelector, now); err != nil {
			return fmt.Errorf("anotar el TXT DKIM publicado: %w", err)
		}
		d.DKIMConfirmedAt = &now
	}
	return nil
}

func recordOK(checks []domain.DNSCheck, kind domain.RecordKind) bool {
	for _, c := range checks {
		if c.Record == kind {
			return c.OK
		}
	}
	return false
}
