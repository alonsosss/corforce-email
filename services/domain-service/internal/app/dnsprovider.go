package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alonsosss/corforce-email/services/domain-service/internal/domain"
	"github.com/alonsosss/corforce-email/services/domain-service/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Publicacion automatica del DNS. La empresa conecta su proveedor una vez con un token de API, que
// se guarda cifrado y solo se descifra en memoria para cada llamada al proveedor. Cada dominio
// publica a mano (por defecto) o en el proveedor conectado; en ese modo la plataforma escribe los
// registros que el dominio necesita en su zona, y solo en ella, sin pisar los del cliente sin su
// confirmacion.

// dnsSession es un token descifrado y las zonas que ve, para una operacion.
type dnsSession struct {
	provider domain.DNSProvider
	api      ports.DNSProviderAPI
	token    domain.APIToken
	zones    []domain.DNSZone
}

// DisconnectDNSProviderResult dice si habia conexion y cuantos dominios volvieron a manual.
type DisconnectDNSProviderResult struct {
	Disconnected bool
	DomainsReset int64
}

// PublishDNSResult es una publicacion y la verificacion que se lanzo despues. VerifyErr es el fallo
// de esa verificacion, que no deshace lo publicado.
type PublishDNSResult struct {
	Domain       *domain.Domain
	Publication  *domain.DNSPublication
	Verification *VerifyResult
	VerifyErr    error
}

// DNSAutomationResult es lo que la publicacion automatica hizo tras rotar o revocar claves DKIM.
// Err no deshace la rotacion ni la revocacion: el cliente ve los registros y puede publicarlos.
type DNSAutomationResult struct {
	Provider    domain.DNSProvider
	Publication *domain.DNSPublication
	Err         error
}

// dnsEnabled dice si el servicio se cableo con publicacion automatica.
func (uc *UseCase) dnsEnabled() bool {
	return uc.dnsRepo != nil && uc.dnsEvents != nil
}

func (uc *UseCase) providerAPI(raw string) (domain.DNSProvider, ports.DNSProviderAPI, error) {
	provider := domain.DNSProvider(raw)
	if !provider.Valid() || !uc.dnsEnabled() {
		return "", nil, domain.ErrUnsupportedDNSProvider
	}
	api, ok := uc.dnsAPIs[provider]
	if !ok || api == nil {
		return "", nil, domain.ErrUnsupportedDNSProvider
	}
	return provider, api, nil
}

// DNSProviderStatus devuelve la conexion de la empresa con el proveedor, o nil si no la tiene.
func (uc *UseCase) DNSProviderStatus(ctx context.Context, tenantID uuid.UUID, provider string) (*domain.DNSProviderConnection, error) {
	p, _, err := uc.providerAPI(provider)
	if err != nil {
		return nil, err
	}
	c, err := uc.dnsRepo.GetDNSProvider(ctx, tenantID, p)
	if errors.Is(err, domain.ErrDNSProviderNotConnected) {
		return nil, nil
	}
	return c, err
}

// ConnectDNSProvider valida el token contra el proveedor (activo y con al menos una zona visible),
// lo guarda cifrado y anuncia la conexion en la misma transaccion. Conectar con otro token
// reemplaza el anterior.
func (uc *UseCase) ConnectDNSProvider(ctx context.Context, tenantID, actorID uuid.UUID, provider, rawToken string) (*domain.DNSProviderConnection, error) {
	p, api, err := uc.providerAPI(provider)
	if err != nil {
		return nil, err
	}
	token, err := domain.NewAPIToken(rawToken)
	if err != nil {
		return nil, err
	}
	if err := api.VerifyToken(ctx, token); err != nil {
		return nil, fmt.Errorf("verificar el token en %s: %w", p, err)
	}
	zones, err := api.ListZones(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("listar las zonas de %s: %w", p, err)
	}
	if len(zones) == 0 {
		return nil, domain.ErrDNSProviderNoZones
	}
	enc, err := uc.cipher.Encrypt([]byte(token.Reveal()))
	if err != nil {
		return nil, fmt.Errorf("cifrar el token de %s: %w", p, err)
	}
	now := uc.now()
	conn := &domain.DNSProviderConnection{
		ID: uuid.New(), TenantID: tenantID, Provider: p, TokenEnc: enc, TokenHint: token.Hint(),
		ConnectedBy: actorID, ConnectedAt: now,
	}
	conn.SetZones(zones, now)
	err = uc.dnsRepo.Transact(ctx, func(ctx context.Context) error {
		if err := uc.dnsRepo.SaveDNSProvider(ctx, conn); err != nil {
			return err
		}
		if err := uc.dnsEvents.DNSProviderConnected(ctx, conn); err != nil {
			return fmt.Errorf("encolar domains.dns_provider.connected: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// DisconnectDNSProvider borra la conexion y su token y devuelve a manual los dominios que
// publicaban en el proveedor, en una transaccion con su evento. Los registros ya publicados se
// quedan en la zona. Sin conexion no hace nada y no falla.
func (uc *UseCase) DisconnectDNSProvider(ctx context.Context, tenantID, actorID uuid.UUID, provider string) (*DisconnectDNSProviderResult, error) {
	p, _, err := uc.providerAPI(provider)
	if err != nil {
		return nil, err
	}
	res := &DisconnectDNSProviderResult{}
	err = uc.dnsRepo.Transact(ctx, func(ctx context.Context) error {
		deleted, err := uc.dnsRepo.DeleteDNSProvider(ctx, tenantID, p)
		if err != nil {
			return err
		}
		reset, err := uc.dnsRepo.ResetDNSMode(ctx, tenantID, domain.DNSMode(p))
		if err != nil {
			return err
		}
		res.Disconnected, res.DomainsReset = deleted, reset
		if !deleted {
			return nil
		}
		if err := uc.dnsEvents.DNSProviderDisconnected(ctx, tenantID, p, actorID, reset, uc.now()); err != nil {
			return fmt.Errorf("encolar domains.dns_provider.disconnected: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// SetDNSMode cambia como se publica el DNS del dominio. Volver a manual siempre se puede y no toca
// la zona. Pasar a un proveedor exige que la empresa lo tenga conectado y que su token vea la zona
// del dominio.
func (uc *UseCase) SetDNSMode(ctx context.Context, tenantID, id uuid.UUID, rawMode string) (*domain.Domain, error) {
	mode := domain.DNSMode(rawMode)
	if !mode.Valid() {
		return nil, domain.ErrInvalidDNSMode
	}
	d, err := uc.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if provider, automatic := mode.Provider(); automatic {
		s, err := uc.openDNSSession(ctx, tenantID, provider)
		if err != nil {
			return nil, err
		}
		if _, err := s.zoneFor(d); err != nil {
			return nil, err
		}
	} else if !uc.dnsEnabled() {
		return nil, domain.ErrUnsupportedDNSProvider
	}
	if err := uc.dnsRepo.SetDNSMode(ctx, tenantID, id, mode); err != nil {
		return nil, err
	}
	d.DNSMode = mode
	return d, nil
}

// ParseRecordKinds valida la lista de registros cuyo reemplazo confirma el cliente.
func ParseRecordKinds(raw []string) (map[domain.RecordKind]bool, error) {
	out := make(map[domain.RecordKind]bool, len(raw))
	for _, k := range raw {
		kind := domain.RecordKind(strings.TrimSpace(k))
		switch kind {
		case domain.RecordOwnershipTXT, domain.RecordMX, domain.RecordSPF, domain.RecordDKIM, domain.RecordDKIMPrevious, domain.RecordDMARC,
			domain.RecordMTASTS, domain.RecordTLSRPT, domain.RecordSESMailFromMX, domain.RecordSESMailFromSPF:
			out[kind] = true
		default:
			return nil, domain.ErrInvalidRecordKind
		}
	}
	return out, nil
}

// PublishDNS crea o actualiza en la zona del proveedor los registros que el dominio necesita y
// lanza la verificacion. Idempotente: lo que ya esta igual no se toca. Un registro del cliente que
// ocupa el sitio de uno de la plataforma (otro SPF, otro MX) queda en conflicto y solo se reemplaza
// si su tipo viene en replace. Se publica con el cerrojo de claves del dominio y su fila leida
// dentro: una rotacion o revocacion simultanea no puede devolver a la zona el TXT de una clave
// revocada. La publicacion y su evento se anotan en la misma transaccion.
func (uc *UseCase) PublishDNS(ctx context.Context, tenantID, id, actorID uuid.UUID, replaceKinds []string) (*PublishDNSResult, error) {
	replace, err := ParseRecordKinds(replaceKinds)
	if err != nil {
		return nil, err
	}
	if !uc.dnsEnabled() {
		return nil, domain.ErrUnsupportedDNSProvider
	}
	seen, err := uc.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	provider, automatic := seen.DNSMode.Provider()
	if !automatic {
		return nil, domain.ErrDNSModeManual
	}
	s, err := uc.openDNSSession(ctx, tenantID, provider)
	if err != nil {
		return nil, err
	}
	var pub *domain.DNSPublication
	mtaSTSPolicyID := uc.mtaSTSPolicyID(ctx, seen)
	err = uc.repo.WithDKIMLock(ctx, tenantID, id, func(ctx context.Context, d *domain.Domain) error {
		if d.DNSMode != seen.DNSMode {
			return domain.ErrDNSModeManual
		}
		zone, err := s.zoneFor(d)
		if err != nil {
			return err
		}
		now := uc.now()
		records, err := uc.applyRecords(ctx, s, zone, domain.DesiredRecords(d, uc.platform, mtaSTSPolicyID), replace)
		if err != nil {
			return err
		}
		pub = &domain.DNSPublication{Provider: provider, Zone: zone.Name, Records: records, PublishedAt: now}
		if pub.Complete() {
			if err := uc.dnsRepo.MarkDNSPublished(ctx, tenantID, id, now); err != nil {
				return err
			}
		}
		if err := uc.dnsEvents.DNSPublished(ctx, d, pub, actorID); err != nil {
			return fmt.Errorf("encolar domains.domain.dns_published: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	res := &PublishDNSResult{Publication: pub}
	verification, err := uc.Verify(ctx, tenantID, id)
	if err != nil {
		uc.logger.Warn("registros publicados en el proveedor DNS pero la verificacion no se completo",
			zap.String("tenant_id", tenantID.String()), zap.String("domain_id", id.String()), zap.Error(err))
		res.VerifyErr = err
		if res.Domain, err = uc.repo.GetByID(ctx, tenantID, id); err != nil {
			return nil, err
		}
		return res, nil
	}
	res.Verification, res.Domain = verification, verification.Domain
	return res, nil
}

// publishDKIMAutomatically lleva a la zona del proveedor el estado de las claves DKIM de un dominio
// en modo automatico, tras rotarlas, revocarlas o retirar la anterior: publica el TXT de la clave
// actual (y el de la anterior en gracia) y retira los TXT de removeSelectors que publico la
// plataforma; los del cliente en esos nombres se dejan y se informan. Devuelve nil en modo manual.
func (uc *UseCase) publishDKIMAutomatically(ctx context.Context, tenantID, id uuid.UUID, removeSelectors []string) *DNSAutomationResult {
	if !uc.dnsEnabled() {
		return nil
	}
	seen, err := uc.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		return nil
	}
	provider, automatic := seen.DNSMode.Provider()
	if !automatic {
		return nil
	}
	res := &DNSAutomationResult{Provider: provider}
	defer func() {
		if res.Err != nil {
			uc.logger.Warn("publicacion automatica de claves DKIM sin completar; el cliente ve los registros en la ficha",
				zap.String("tenant_id", tenantID.String()), zap.String("domain", seen.Domain), zap.Error(res.Err))
		}
	}()
	s, err := uc.openDNSSession(ctx, tenantID, provider)
	if err != nil {
		res.Err = err
		return res
	}
	res.Err = uc.repo.WithDKIMLock(ctx, tenantID, id, func(ctx context.Context, d *domain.Domain) error {
		if d.DNSMode != seen.DNSMode {
			return domain.ErrDNSModeManual
		}
		zone, err := s.zoneFor(d)
		if err != nil {
			return err
		}
		var desired []domain.DesiredRecord
		for _, dr := range domain.DesiredRecords(d, uc.platform, "") {
			if dr.Kind == domain.RecordDKIM || dr.Kind == domain.RecordDKIMPrevious {
				desired = append(desired, dr)
			}
		}
		pub := &domain.DNSPublication{Provider: provider, Zone: zone.Name, PublishedAt: uc.now()}
		res.Publication = pub
		if pub.Records, err = uc.applyRecords(ctx, s, zone, desired, nil); err != nil {
			return err
		}
		hosts := make([]string, 0, len(removeSelectors))
		for _, selector := range removeSelectors {
			if selector != d.DKIMSelector && selector != d.DKIMPreviousSelector {
				hosts = append(hosts, domain.DKIMHost(selector, d.Domain))
			}
		}
		if pub.Removed, pub.Kept, err = uc.removeManaged(ctx, s, zone, hosts); err != nil {
			return err
		}
		if err := uc.dnsEvents.DNSPublished(ctx, d, pub, uuid.Nil); err != nil {
			return fmt.Errorf("encolar domains.domain.dns_published: %w", err)
		}
		return nil
	})
	return res
}

// openDNSSession descifra el token de la empresa y lista las zonas que ve, que se anotan como su
// ultima validacion.
func (uc *UseCase) openDNSSession(ctx context.Context, tenantID uuid.UUID, provider domain.DNSProvider) (*dnsSession, error) {
	p, api, err := uc.providerAPI(string(provider))
	if err != nil {
		return nil, err
	}
	conn, err := uc.dnsRepo.GetDNSProvider(ctx, tenantID, p)
	if err != nil {
		return nil, err
	}
	plain, err := uc.cipher.Decrypt(conn.TokenEnc)
	if err != nil {
		return nil, fmt.Errorf("descifrar el token de %s: %w", p, err)
	}
	token, err := domain.NewAPIToken(string(plain))
	if err != nil {
		return nil, domain.ErrDNSProviderTokenInvalid
	}
	zones, err := api.ListZones(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("listar las zonas de %s: %w", p, err)
	}
	conn.SetZones(zones, uc.now())
	if err := uc.dnsRepo.UpdateDNSProviderZones(ctx, conn); err != nil {
		uc.logger.Warn("no se pudo anotar la validacion del proveedor DNS",
			zap.String("tenant_id", tenantID.String()), zap.String("provider", string(p)), zap.Error(err))
	}
	return &dnsSession{provider: p, api: api, token: token, zones: zones}, nil
}

func (s *dnsSession) zoneFor(d *domain.Domain) (domain.DNSZone, error) {
	zone, ok := domain.ZoneForDomain(d.Domain, s.zones)
	if !ok {
		return domain.DNSZone{}, domain.ErrDNSZoneNotFound
	}
	return zone, nil
}

// abortsPublication dice si un fallo del proveedor afecta a toda la publicacion (credencial,
// limite, disponibilidad, zona) y no solo al registro.
func abortsPublication(err error) bool {
	for _, target := range []error{
		domain.ErrDNSProviderTokenInvalid, domain.ErrDNSProviderPermissionDenied, domain.ErrDNSZoneNotFound,
		domain.ErrDNSProviderRateLimited, domain.ErrDNSProviderUnavailable, domain.ErrDNSRecordOutsideZone,
		context.Canceled, context.DeadlineExceeded,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// applyRecords planifica y escribe cada registro en la zona. Un rechazo del proveedor marca el
// registro como fallido y sigue con el resto; un fallo que afecta a toda la publicacion la corta.
func (uc *UseCase) applyRecords(ctx context.Context, s *dnsSession, zone domain.DNSZone, desired []domain.DesiredRecord, replace map[domain.RecordKind]bool) ([]domain.RecordResult, error) {
	results := make([]domain.RecordResult, 0, len(desired))
	for _, dr := range desired {
		if !domain.HostInZone(dr.Name, zone.Name) {
			return nil, domain.ErrDNSRecordOutsideZone
		}
		existing, err := s.api.ListRecords(ctx, s.token, zone, dr.Type, dr.Name)
		if err != nil {
			return nil, fmt.Errorf("leer %s %s: %w", dr.Type, dr.Name, err)
		}
		plan := domain.PlanRecord(dr, existing, replace[dr.Kind])
		res := domain.RecordResult{
			Kind: dr.Kind, Type: dr.Type, Name: dr.Name, Content: dr.Content, Priority: dr.Priority, Action: plan.Action,
		}
		for _, f := range plan.Foreign {
			res.Existing = append(res.Existing, domain.ExistingValue(f))
		}
		if err := s.execute(ctx, zone, plan); err != nil {
			if abortsPublication(err) {
				return nil, fmt.Errorf("escribir %s %s: %w", dr.Type, dr.Name, err)
			}
			res.Action, res.Err = domain.RecordFailed, err
		}
		results = append(results, res)
	}
	return results, nil
}

// execute escribe un plan. Antes de cada escritura comprueba que el registro es de la zona.
func (s *dnsSession) execute(ctx context.Context, zone domain.DNSZone, plan domain.RecordPlan) error {
	if plan.Action == domain.RecordConflict || plan.Action == domain.RecordUnchanged {
		return nil
	}
	if plan.Create {
		err := s.api.CreateRecord(ctx, s.token, zone, plan.Desired.ProviderRecord(""))
		if errors.Is(err, domain.ErrDNSRecordExists) {
			return nil
		}
		return err
	}
	if plan.Update != nil {
		if plan.Update.ID == "" || !domain.HostInZone(plan.Update.Name, zone.Name) {
			return domain.ErrDNSRecordOutsideZone
		}
		if err := s.api.UpdateRecord(ctx, s.token, zone, plan.Desired.ProviderRecord(plan.Update.ID)); err != nil {
			return err
		}
	}
	for _, r := range plan.Delete {
		if r.ID == "" || !domain.HostInZone(r.Name, zone.Name) {
			return domain.ErrDNSRecordOutsideZone
		}
		if err := s.api.DeleteRecord(ctx, s.token, zone, r.ID); err != nil {
			return err
		}
	}
	return nil
}

// removeManaged retira los TXT de esos nombres que publico la plataforma y cuenta los del cliente.
func (uc *UseCase) removeManaged(ctx context.Context, s *dnsSession, zone domain.DNSZone, hosts []string) (removed, kept []string, err error) {
	for _, host := range hosts {
		if !domain.HostInZone(host, zone.Name) {
			return removed, kept, domain.ErrDNSRecordOutsideZone
		}
		records, err := s.api.ListRecords(ctx, s.token, zone, "TXT", host)
		if err != nil {
			return removed, kept, fmt.Errorf("leer TXT %s: %w", host, err)
		}
		var didRemove, didKeep bool
		for _, r := range records {
			if !strings.EqualFold(r.Type, "TXT") || !domain.SameHost(r.Name, host) {
				continue
			}
			if !domain.HostInZone(r.Name, zone.Name) || r.ID == "" {
				return removed, kept, domain.ErrDNSRecordOutsideZone
			}
			if !r.Managed() {
				didKeep = true
				continue
			}
			if err := s.api.DeleteRecord(ctx, s.token, zone, r.ID); err != nil {
				return removed, kept, fmt.Errorf("retirar TXT %s: %w", host, err)
			}
			didRemove = true
		}
		if didRemove {
			removed = append(removed, host)
		}
		if didKeep {
			kept = append(kept, host)
		}
	}
	return removed, kept, nil
}
