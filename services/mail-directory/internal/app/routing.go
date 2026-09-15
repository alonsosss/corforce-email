package app

import (
	"context"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

// ── Sender ACL ────────────────────────────────────────────────────────────────

type SenderACLRequest struct {
	LoggedInAs string
	SendAs     string
	External   bool
}

type UpdateSenderACLRequest struct {
	SendAs   *string
	External *bool
}

// normalizeSendAs valida lo que un buzon puede poner como remitente: una direccion, un
// '@dominio' o '*'. Sin external, direccion y dominio deben ser de la empresa.
func (uc *UseCase) normalizeSendAs(ctx context.Context, tenantID uuid.UUID, raw string, external bool) (string, error) {
	sendAs := strings.ToLower(strings.TrimSpace(raw))
	if sendAs == "*" {
		if !external {
			return "", domain.ErrWildcardNeedsExtnl
		}
		return sendAs, nil
	}
	address, domainPart, err := domain.NormalizeAddress(sendAs)
	if err != nil {
		return "", err
	}
	if !external {
		if err := uc.ownsDomainOrAlias(ctx, tenantID, domainPart); err != nil {
			return "", err
		}
	}
	return address, nil
}

func (uc *UseCase) ListSenderACL(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.SenderACL, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.senderACL.List(ctx, tenantID, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetSenderACL(ctx context.Context, tenantID, id uuid.UUID) (a *domain.SenderACL, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		a, err = uc.senderACL.Get(ctx, tenantID, id)
		return err
	})
	return a, err
}

func (uc *UseCase) CreateSenderACL(ctx context.Context, tenantID uuid.UUID, req SenderACLRequest) (*domain.SenderACL, error) {
	loggedInAs, _, err := domain.NormalizeEmail(req.LoggedInAs)
	if err != nil {
		return nil, err
	}
	a := &domain.SenderACL{ID: uuid.New(), TenantID: tenantID, LoggedInAs: loggedInAs, External: req.External}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if _, err := uc.ownMailbox(ctx, tenantID, loggedInAs); err != nil {
			return err
		}
		sendAs, err := uc.normalizeSendAs(ctx, tenantID, req.SendAs, req.External)
		if err != nil {
			return err
		}
		a.SendAs = sendAs
		return uc.senderACL.Create(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (uc *UseCase) UpdateSenderACL(ctx context.Context, tenantID, id uuid.UUID, req UpdateSenderACLRequest) (*domain.SenderACL, error) {
	if req.SendAs == nil && req.External == nil {
		return nil, domain.ErrNothingToUpdate
	}
	var a *domain.SenderACL
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		var err error
		a, err = uc.senderACL.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		a.External = boolOr(req.External, a.External)
		sendAs := a.SendAs
		if req.SendAs != nil {
			sendAs = *req.SendAs
		}
		if a.SendAs, err = uc.normalizeSendAs(ctx, tenantID, sendAs, a.External); err != nil {
			return err
		}
		return uc.senderACL.Update(ctx, a)
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (uc *UseCase) DeleteSenderACL(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if _, err := uc.senderACL.Get(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.senderACL.Delete(ctx, tenantID, id)
	})
}

// ── Relayhosts ────────────────────────────────────────────────────────────────

type CreateRelayhostRequest struct {
	Hostname string
	Username string
	Password string
	Active   *bool
}

type UpdateRelayhostRequest struct {
	Hostname *string
	Username *string
	Password *string
	Active   *bool
}

func (uc *UseCase) ListRelayhosts(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.Relayhost, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.relayhosts.List(ctx, tenantID, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetRelayhost(ctx context.Context, tenantID, id uuid.UUID) (r *domain.Relayhost, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		r, err = uc.relayhosts.Get(ctx, tenantID, id)
		return err
	})
	return r, err
}

func (uc *UseCase) CreateRelayhost(ctx context.Context, tenantID uuid.UUID, req CreateRelayhostRequest) (*domain.Relayhost, error) {
	host, err := domain.NormalizeHostname(req.Hostname)
	if err != nil {
		return nil, err
	}
	r := &domain.Relayhost{
		ID: uuid.New(), TenantID: tenantID, Hostname: host, Username: strings.TrimSpace(req.Username),
		HasPassword: req.Password != "", Active: boolOr(req.Active, true),
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		return uc.relayhosts.Create(ctx, r, req.Password)
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (uc *UseCase) UpdateRelayhost(ctx context.Context, tenantID, id uuid.UUID, req UpdateRelayhostRequest) (*domain.Relayhost, error) {
	if req.Hostname == nil && req.Username == nil && req.Password == nil && req.Active == nil {
		return nil, domain.ErrNothingToUpdate
	}
	var r *domain.Relayhost
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		var err error
		r, err = uc.relayhosts.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if req.Hostname != nil {
			if r.Hostname, err = domain.NormalizeHostname(*req.Hostname); err != nil {
				return err
			}
		}
		if req.Username != nil {
			r.Username = strings.TrimSpace(*req.Username)
		}
		if req.Password != nil {
			r.HasPassword = *req.Password != ""
		}
		r.Active = boolOr(req.Active, r.Active)
		return uc.relayhosts.Update(ctx, r, req.Password)
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (uc *UseCase) DeleteRelayhost(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if _, err := uc.relayhosts.Get(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.relayhosts.Delete(ctx, tenantID, id)
	})
}

// ── Transportes ───────────────────────────────────────────────────────────────

type CreateTransportRequest struct {
	Destination string
	Nexthop     string
	Username    string
	Password    string
	IsMXBased   bool
	Active      *bool
	// Platform crea una ruta sin empresa (tenant_id NULL): solo el operador de la plataforma.
	Platform bool
}

type UpdateTransportRequest struct {
	Destination *string
	Nexthop     *string
	Username    *string
	Password    *string
	IsMXBased   *bool
	Active      *bool
}

func (r UpdateTransportRequest) empty() bool {
	return r.Destination == nil && r.Nexthop == nil && r.Username == nil && r.Password == nil &&
		r.IsMXBased == nil && r.Active == nil
}

func normalizeTransportField(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	if v == "" || len(v) > 255 || strings.ContainsAny(v, " \t\r\n") {
		return "", domain.ErrInvalidHostname
	}
	return v, nil
}

func (uc *UseCase) ListTransports(ctx context.Context, scope Scope, page ports.Page) (items []domain.Transport, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.transports.List(ctx, scope.TenantID, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetTransport(ctx context.Context, scope Scope, id uuid.UUID) (t *domain.Transport, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		t, err = uc.transports.Get(ctx, scope.transportScope(), id)
		return err
	})
	return t, err
}

func (uc *UseCase) CreateTransport(ctx context.Context, scope Scope, req CreateTransportRequest) (*domain.Transport, error) {
	if req.Platform && !scope.Platform {
		return nil, domain.ErrPlatformOnly
	}
	dest, err := normalizeTransportField(req.Destination)
	if err != nil {
		return nil, err
	}
	nexthop, err := normalizeTransportField(req.Nexthop)
	if err != nil {
		return nil, err
	}
	t := &domain.Transport{
		ID: uuid.New(), Destination: dest, Nexthop: nexthop, Username: strings.TrimSpace(req.Username),
		HasPassword: req.Password != "", IsMXBased: req.IsMXBased, Active: boolOr(req.Active, true),
	}
	if !req.Platform {
		tenantID := scope.TenantID
		t.TenantID = &tenantID
	}
	err = uc.writeTx(ctx, scope.TenantID, func(ctx context.Context) error {
		return uc.transports.Create(ctx, t, req.Password)
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (uc *UseCase) UpdateTransport(ctx context.Context, scope Scope, id uuid.UUID, req UpdateTransportRequest) (*domain.Transport, error) {
	if req.empty() {
		return nil, domain.ErrNothingToUpdate
	}
	var t *domain.Transport
	err := uc.writeTx(ctx, scope.TenantID, func(ctx context.Context) error {
		var err error
		t, err = uc.transports.Get(ctx, scope.transportScope(), id)
		if err != nil {
			return err
		}
		if t.IsPlatform() && !scope.Platform {
			return domain.ErrPlatformOnly
		}
		if req.Destination != nil {
			if t.Destination, err = normalizeTransportField(*req.Destination); err != nil {
				return err
			}
		}
		if req.Nexthop != nil {
			if t.Nexthop, err = normalizeTransportField(*req.Nexthop); err != nil {
				return err
			}
		}
		if req.Username != nil {
			t.Username = strings.TrimSpace(*req.Username)
		}
		if req.Password != nil {
			t.HasPassword = *req.Password != ""
		}
		t.IsMXBased = boolOr(req.IsMXBased, t.IsMXBased)
		t.Active = boolOr(req.Active, t.Active)
		return uc.transports.Update(ctx, scope.transportScope(), t, req.Password)
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (uc *UseCase) DeleteTransport(ctx context.Context, scope Scope, id uuid.UUID) error {
	return uc.writeTx(ctx, scope.TenantID, func(ctx context.Context) error {
		t, err := uc.transports.Get(ctx, scope.transportScope(), id)
		if err != nil {
			return err
		}
		if t.IsPlatform() && !scope.Platform {
			return domain.ErrPlatformOnly
		}
		return uc.transports.Delete(ctx, scope.transportScope(), id)
	})
}

// ── Politicas TLS ─────────────────────────────────────────────────────────────

type CreateTLSPolicyRequest struct {
	Dest       string
	Policy     string
	Parameters string
	Active     *bool
}

type UpdateTLSPolicyRequest struct {
	Policy     *string
	Parameters *string
	Active     *bool
}

func (uc *UseCase) ListTLSPolicies(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.TLSPolicy, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.tlsPolicies.List(ctx, tenantID, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetTLSPolicy(ctx context.Context, tenantID, id uuid.UUID) (p *domain.TLSPolicy, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		p, err = uc.tlsPolicies.Get(ctx, tenantID, id)
		return err
	})
	return p, err
}

func (uc *UseCase) CreateTLSPolicy(ctx context.Context, tenantID uuid.UUID, req CreateTLSPolicyRequest) (*domain.TLSPolicy, error) {
	dest, err := normalizeTransportField(req.Dest)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateTLSPolicy(req.Policy); err != nil {
		return nil, err
	}
	p := &domain.TLSPolicy{
		ID: uuid.New(), TenantID: tenantID, Dest: dest, Policy: req.Policy,
		Parameters: strings.TrimSpace(req.Parameters), Active: boolOr(req.Active, true),
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error { return uc.tlsPolicies.Create(ctx, p) })
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (uc *UseCase) UpdateTLSPolicy(ctx context.Context, tenantID, id uuid.UUID, req UpdateTLSPolicyRequest) (*domain.TLSPolicy, error) {
	if req.Policy == nil && req.Parameters == nil && req.Active == nil {
		return nil, domain.ErrNothingToUpdate
	}
	if req.Policy != nil {
		if err := domain.ValidateTLSPolicy(*req.Policy); err != nil {
			return nil, err
		}
	}
	var p *domain.TLSPolicy
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		var err error
		p, err = uc.tlsPolicies.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if req.Policy != nil {
			p.Policy = *req.Policy
		}
		if req.Parameters != nil {
			p.Parameters = strings.TrimSpace(*req.Parameters)
		}
		p.Active = boolOr(req.Active, p.Active)
		return uc.tlsPolicies.Update(ctx, p)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (uc *UseCase) DeleteTLSPolicy(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if _, err := uc.tlsPolicies.Get(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.tlsPolicies.Delete(ctx, tenantID, id)
	})
}

// ── Mapas de destinatario ─────────────────────────────────────────────────────

type CreateRecipientMapRequest struct {
	OldDest string
	NewDest string
	Active  *bool
}

type UpdateRecipientMapRequest struct {
	NewDest *string
	Active  *bool
}

func (uc *UseCase) ListRecipientMaps(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.RecipientMap, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.recipientMap.List(ctx, tenantID, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetRecipientMap(ctx context.Context, tenantID, id uuid.UUID) (m *domain.RecipientMap, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err = uc.recipientMap.Get(ctx, tenantID, id)
		return err
	})
	return m, err
}

// CreateRecipientMap reescribe un destinatario (direccion o '@dominio' de la empresa)
// hacia otra direccion, propia o externa.
func (uc *UseCase) CreateRecipientMap(ctx context.Context, tenantID uuid.UUID, req CreateRecipientMapRequest) (*domain.RecipientMap, error) {
	oldDest, domainPart, err := domain.NormalizeAddress(req.OldDest)
	if err != nil {
		return nil, err
	}
	newDest, _, err := domain.NormalizeEmail(req.NewDest)
	if err != nil {
		return nil, err
	}
	m := &domain.RecipientMap{ID: uuid.New(), TenantID: tenantID, OldDest: oldDest, NewDest: newDest, Active: boolOr(req.Active, true)}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if err := uc.ownsDomainOrAlias(ctx, tenantID, domainPart); err != nil {
			return err
		}
		return uc.recipientMap.Create(ctx, m)
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (uc *UseCase) UpdateRecipientMap(ctx context.Context, tenantID, id uuid.UUID, req UpdateRecipientMapRequest) (*domain.RecipientMap, error) {
	if req.NewDest == nil && req.Active == nil {
		return nil, domain.ErrNothingToUpdate
	}
	var newDest string
	if req.NewDest != nil {
		var err error
		if newDest, _, err = domain.NormalizeEmail(*req.NewDest); err != nil {
			return nil, err
		}
	}
	var m *domain.RecipientMap
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		var err error
		m, err = uc.recipientMap.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if req.NewDest != nil {
			m.NewDest = newDest
		}
		m.Active = boolOr(req.Active, m.Active)
		return uc.recipientMap.Update(ctx, m)
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (uc *UseCase) DeleteRecipientMap(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if _, err := uc.recipientMap.Get(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.recipientMap.Delete(ctx, tenantID, id)
	})
}

// ── Mapas BCC ─────────────────────────────────────────────────────────────────

type CreateBCCMapRequest struct {
	LocalDest string
	BCCDest   string
	Type      string
	Active    *bool
}

type UpdateBCCMapRequest struct {
	BCCDest *string
	Type    *string
	Active  *bool
}

func (uc *UseCase) ListBCCMaps(ctx context.Context, tenantID uuid.UUID, page ports.Page) (items []domain.BCCMap, total int64, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.bccMaps.List(ctx, tenantID, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetBCCMap(ctx context.Context, tenantID, id uuid.UUID) (m *domain.BCCMap, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err = uc.bccMaps.Get(ctx, tenantID, id)
		return err
	})
	return m, err
}

func (uc *UseCase) CreateBCCMap(ctx context.Context, tenantID uuid.UUID, req CreateBCCMapRequest) (*domain.BCCMap, error) {
	localDest, domainPart, err := domain.NormalizeAddress(req.LocalDest)
	if err != nil {
		return nil, err
	}
	bccDest, _, err := domain.NormalizeEmail(req.BCCDest)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidateBCCType(req.Type); err != nil {
		return nil, err
	}
	m := &domain.BCCMap{
		ID: uuid.New(), TenantID: tenantID, LocalDest: localDest, BCCDest: bccDest, Domain: domainPart,
		Type: req.Type, Active: boolOr(req.Active, false),
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if err := uc.ownsDomainOrAlias(ctx, tenantID, domainPart); err != nil {
			return err
		}
		return uc.bccMaps.Create(ctx, m)
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (uc *UseCase) UpdateBCCMap(ctx context.Context, tenantID, id uuid.UUID, req UpdateBCCMapRequest) (*domain.BCCMap, error) {
	if req.BCCDest == nil && req.Type == nil && req.Active == nil {
		return nil, domain.ErrNothingToUpdate
	}
	var bccDest string
	if req.BCCDest != nil {
		var err error
		if bccDest, _, err = domain.NormalizeEmail(*req.BCCDest); err != nil {
			return nil, err
		}
	}
	if req.Type != nil {
		if err := domain.ValidateBCCType(*req.Type); err != nil {
			return nil, err
		}
	}
	var m *domain.BCCMap
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		var err error
		m, err = uc.bccMaps.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if req.BCCDest != nil {
			m.BCCDest = bccDest
		}
		if req.Type != nil {
			m.Type = *req.Type
		}
		m.Active = boolOr(req.Active, m.Active)
		return uc.bccMaps.Update(ctx, m)
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (uc *UseCase) DeleteBCCMap(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if _, err := uc.bccMaps.Get(ctx, tenantID, id); err != nil {
			return err
		}
		return uc.bccMaps.Delete(ctx, tenantID, id)
	})
}
