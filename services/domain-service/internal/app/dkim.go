package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// selectorPrefix identifica las claves emitidas por la plataforma en la zona del
// cliente: cfm202609, cfm20260912, ...
const selectorPrefix = "cfm"

// dkimSelector devuelve el selector del mes; si coincide con alguno en uso (dos
// rotaciones en el mismo mes) afina a dia y luego a segundo. Un selector repetido
// pisaria el TXT del anterior en la zona del cliente.
func dkimSelector(now time.Time, inUse ...string) string {
	now = now.UTC()
	for _, layout := range []string{"200601", "20060102", "20060102150405"} {
		candidate := selectorPrefix + now.Format(layout)
		taken := false
		for _, s := range inUse {
			if s == candidate {
				taken = true
				break
			}
		}
		if !taken {
			return candidate
		}
	}
	return selectorPrefix + now.Format("20060102150405")
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

// assignDKIMKey genera un par nuevo y lo deja cifrado en el dominio como clave actual.
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
	return nil
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

// RotateDKIMResult es la respuesta de una rotacion: el TXT nuevo que hay que publicar y
// hasta cuando se conserva el selector anterior.
type RotateDKIMResult struct {
	Domain     *domain.Domain
	Record     domain.DNSRecord
	GraceUntil time.Time
}

// RotateDKIM genera un par nuevo y conserva el anterior durante la gracia. El firmado no
// cambia hasta que una verificacion vea publicado el TXT del selector nuevo; hasta
// entonces los motores siguen con el anterior. Si habia otra clave en gracia, se retira:
// solo se custodian dos.
func (uc *UseCase) RotateDKIM(ctx context.Context, tenantID, id uuid.UUID) (*RotateDKIMResult, error) {
	d, err := uc.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	now := uc.now()
	retired := ""
	if d.HasPreviousDKIM() {
		retired = d.DKIMPreviousSelector
	}
	previous := domain.Domain{
		DKIMSelector: d.DKIMSelector, DKIMPrivateKeyEnc: d.DKIMPrivateKeyEnc, DKIMPublicKey: d.DKIMPublicKey,
	}
	if err := uc.assignDKIMKey(d, dkimSelector(now, previous.DKIMSelector, retired)); err != nil {
		return nil, err
	}
	d.DKIMPreviousSelector = previous.DKIMSelector
	d.DKIMPreviousPrivateKeyEnc = previous.DKIMPrivateKeyEnc
	d.DKIMPreviousPublicKey = previous.DKIMPublicKey
	d.DKIMRotatedAt = &now
	if err := uc.repo.Update(ctx, d); err != nil {
		return nil, err
	}

	if d.Status == domain.StatusVerified {
		if retired != "" {
			if err := uc.mailSecurity.RetireDKIM(ctx, tenantID, d.Domain, retired); err != nil {
				uc.logger.Warn("no se pudo retirar el selector anterior en mail-security; se reintenta en el barrido",
					zap.String("domain", d.Domain), zap.String("selector", retired), zap.Error(err))
			}
		}
		// El TXT nuevo no esta publicado todavia: la clave nueva se deposita pero se
		// sigue firmando con la anterior.
		if err := uc.publishDKIM(ctx, d, true); err != nil {
			uc.logger.Warn("no se pudieron publicar las claves DKIM; se reintenta en el barrido",
				zap.String("domain", d.Domain), zap.Error(err))
		}
	}
	uc.publish("domains.domain.dkim_rotated", d, func() error { return uc.events.DKIMRotated(ctx, d) })

	return &RotateDKIMResult{
		Domain: d,
		Record: domain.DNSRecord{
			Record: domain.RecordDKIM, Type: "TXT", Required: true,
			Host: domain.DKIMHost(d.DKIMSelector, d.Domain), Value: domain.DKIMValue(d.DKIMPublicKey),
		},
		GraceUntil: now.Add(uc.rotationGrace),
	}, nil
}

func (uc *UseCase) publishDKIM(ctx context.Context, d *domain.Domain, signWithPrevious bool) error {
	keys, err := uc.signingKeys(d, signWithPrevious)
	if err != nil {
		return err
	}
	return uc.mailSecurity.PublishDKIM(ctx, d.TenantID, d.Domain, keys)
}

// retirePreviousDKIM saca de gracia la clave anterior: la retira de mail-security y
// limpia las columnas. Si mail-security no responde, se deja para el siguiente barrido.
func (uc *UseCase) retirePreviousDKIM(ctx context.Context, d *domain.Domain) error {
	selector := d.DKIMPreviousSelector
	if err := uc.mailSecurity.RetireDKIM(ctx, d.TenantID, d.Domain, selector); err != nil {
		return fmt.Errorf("retirar selector %s en mail-security: %w", selector, err)
	}
	d.ClearPreviousDKIM()
	return uc.repo.Update(ctx, d)
}
