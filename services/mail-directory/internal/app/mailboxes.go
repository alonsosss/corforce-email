package app

import (
	"context"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-directory/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-directory/internal/ports"
	"github.com/google/uuid"
)

type CreateMailboxRequest struct {
	LocalPart   string
	Domain      string
	Password    string
	DisplayName string
	// QuotaBytes nil toma default_quota_bytes del dominio.
	QuotaBytes    *int64
	Active        *int
	Kind          string
	TLSEnforceIn  bool
	TLSEnforceOut bool
	IMAPAccess    *bool
	POP3Access    *bool
	SMTPAccess    *bool
	SieveAccess   *bool
	DAVAccess     *bool
	ForcePwUpdate bool
	RelayhostID   *uuid.UUID
}

type UpdateMailboxRequest struct {
	DisplayName    *string
	QuotaBytes     *int64
	Active         *int
	Kind           *string
	TLSEnforceIn   *bool
	TLSEnforceOut  *bool
	IMAPAccess     *bool
	POP3Access     *bool
	SMTPAccess     *bool
	SieveAccess    *bool
	DAVAccess      *bool
	ForcePwUpdate  *bool
	RelayhostID    *uuid.UUID
	ClearRelayhost bool
}

func (r UpdateMailboxRequest) empty() bool {
	return r.DisplayName == nil && r.QuotaBytes == nil && r.Active == nil && r.Kind == nil &&
		r.TLSEnforceIn == nil && r.TLSEnforceOut == nil && r.IMAPAccess == nil && r.POP3Access == nil &&
		r.SMTPAccess == nil && r.SieveAccess == nil && r.DAVAccess == nil && r.ForcePwUpdate == nil && r.RelayhostID == nil && !r.ClearRelayhost
}

func boolOr(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// ListMailboxes pagina los buzones de la empresa. filter.Search busca por subcadena en
// username y nombre visible sin distinguir mayusculas; filter.Domain es exacto.
func (uc *UseCase) ListMailboxes(ctx context.Context, tenantID uuid.UUID, filter ports.MailboxFilter, page ports.Page) (items []domain.Mailbox, total int64, err error) {
	if filter.Search, err = normalizeSearch(filter.Search); err != nil {
		return nil, 0, err
	}
	if filter.Domain = strings.TrimSpace(filter.Domain); filter.Domain != "" {
		if filter.Domain, err = domain.NormalizeDomain(filter.Domain); err != nil {
			return nil, 0, err
		}
	}
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		items, total, err = uc.mailboxes.List(ctx, tenantID, filter, page)
		return err
	})
	return items, total, err
}

func (uc *UseCase) GetMailbox(ctx context.Context, tenantID, id uuid.UUID) (m *domain.Mailbox, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err = uc.mailboxes.Get(ctx, tenantID, id)
		return err
	})
	return m, err
}

func (uc *UseCase) CreateMailbox(ctx context.Context, tenantID uuid.UUID, req CreateMailboxRequest) (*domain.Mailbox, error) {
	local, err := domain.NormalizeLocalPart(req.LocalPart)
	if err != nil {
		return nil, err
	}
	name, err := domain.NormalizeDomain(req.Domain)
	if err != nil {
		return nil, err
	}
	if err := domain.ValidatePassword(req.Password); err != nil {
		return nil, err
	}
	if err := domain.ValidateKind(req.Kind); err != nil {
		return nil, err
	}
	active := domain.ActiveOn
	if req.Active != nil {
		active = *req.Active
	}
	if err := domain.ValidateActive(active); err != nil {
		return nil, err
	}
	hash, err := uc.secrets.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}
	m := &domain.Mailbox{
		ID: uuid.New(), TenantID: tenantID, Username: local + "@" + name, LocalPart: local, Domain: name,
		PasswordHash: hash, DisplayName: strings.TrimSpace(req.DisplayName), Active: active, Kind: req.Kind,
		TLSEnforceIn: req.TLSEnforceIn, TLSEnforceOut: req.TLSEnforceOut, RelayhostID: req.RelayhostID,
		IMAPAccess: boolOr(req.IMAPAccess, true), POP3Access: boolOr(req.POP3Access, true),
		SMTPAccess: boolOr(req.SMTPAccess, true), SieveAccess: boolOr(req.SieveAccess, true),
		DAVAccess: boolOr(req.DAVAccess, true), ForcePwUpdate: req.ForcePwUpdate,
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		d, err := uc.ownDomain(ctx, tenantID, name)
		if err != nil {
			return err
		}
		if err := uc.ownRelayhost(ctx, tenantID, m.RelayhostID); err != nil {
			return err
		}
		count, err := uc.mailboxes.CountByDomain(ctx, tenantID, name)
		if err != nil {
			return err
		}
		if err := domain.CheckLimit(d.MaxMailboxes, count, domain.ErrMaxMailboxesReached); err != nil {
			return err
		}
		m.QuotaBytes = d.DefaultQuotaBytes
		if req.QuotaBytes != nil {
			m.QuotaBytes = *req.QuotaBytes
		}
		if err := uc.checkQuota(ctx, d, m); err != nil {
			return err
		}
		if err := uc.addressFree(ctx, tenantID, m.Username); err != nil {
			return err
		}
		if err := uc.mailboxes.Create(ctx, m); err != nil {
			return err
		}
		return uc.events.MailboxCreated(ctx, m)
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (uc *UseCase) checkQuota(ctx context.Context, d *domain.Domain, m *domain.Mailbox) error {
	used, err := uc.mailboxes.QuotaSumByDomain(ctx, m.TenantID, d.Domain, m.ID)
	if err != nil {
		return err
	}
	return domain.CheckMailboxQuota(m.QuotaBytes, domainLimits(d), used)
}

func (uc *UseCase) UpdateMailbox(ctx context.Context, tenantID, id uuid.UUID, req UpdateMailboxRequest) (*domain.Mailbox, error) {
	if req.empty() {
		return nil, domain.ErrNothingToUpdate
	}
	if req.Active != nil {
		if err := domain.ValidateActive(*req.Active); err != nil {
			return nil, err
		}
	}
	if req.Kind != nil {
		if err := domain.ValidateKind(*req.Kind); err != nil {
			return nil, err
		}
	}
	var m *domain.Mailbox
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		var err error
		m, err = uc.mailboxes.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		before := *m
		applyMailboxUpdate(m, req)
		if err := uc.ownRelayhost(ctx, tenantID, m.RelayhostID); err != nil {
			return err
		}
		if req.QuotaBytes != nil {
			d, err := uc.ownDomain(ctx, tenantID, m.Domain)
			if err != nil {
				return err
			}
			if err := uc.checkQuota(ctx, d, m); err != nil {
				return err
			}
		}
		if err := uc.mailboxes.Update(ctx, m); err != nil {
			return err
		}
		// Un buzon apagado no puede iniciar sesion; sus contrasenas de aplicacion se
		// revocan para que un cliente configurado no siga entrando al reactivarlo.
		if m.Active == domain.ActiveOff && before.Active != domain.ActiveOff {
			if _, err := uc.appPasswords.DeactivateByMailbox(ctx, tenantID, id); err != nil {
				return err
			}
		}
		// Los dos avisos llevan los atributos que cambiaron: con ellos el webmail cierra sus
		// sesiones solo cuando alguno las invalida (el buzon deja de poder entrar o pierde imap o
		// smtp) y las conserva ante un cambio de cuota o de nombre visible.
		changed := domain.MailboxChanges(before, *m)
		// Lo que le quita al buzon un inicio de sesion lo pierden todas sus credenciales y sale como
		// la principal: mail-security cierra en Dovecot las sesiones ya abiertas, que
		// mail.mailbox.updated con el buzon activo solo vaciaria, aunque el buzon se reactive antes
		// de que atienda el cambio; el webmail cierra las suyas.
		if domain.MailboxLoginsRevoked(before, *m) {
			if err := uc.events.MailboxCredentialsChanged(ctx, m, domain.CredentialPassword, changed); err != nil {
				return err
			}
		}
		return uc.events.MailboxUpdated(ctx, m, changed)
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func applyMailboxUpdate(m *domain.Mailbox, req UpdateMailboxRequest) {
	if req.DisplayName != nil {
		m.DisplayName = strings.TrimSpace(*req.DisplayName)
	}
	if req.QuotaBytes != nil {
		m.QuotaBytes = *req.QuotaBytes
	}
	if req.Active != nil {
		m.Active = *req.Active
	}
	if req.Kind != nil {
		m.Kind = *req.Kind
	}
	if req.TLSEnforceIn != nil {
		m.TLSEnforceIn = *req.TLSEnforceIn
	}
	if req.TLSEnforceOut != nil {
		m.TLSEnforceOut = *req.TLSEnforceOut
	}
	if req.IMAPAccess != nil {
		m.IMAPAccess = *req.IMAPAccess
	}
	if req.POP3Access != nil {
		m.POP3Access = *req.POP3Access
	}
	if req.SMTPAccess != nil {
		m.SMTPAccess = *req.SMTPAccess
	}
	if req.SieveAccess != nil {
		m.SieveAccess = *req.SieveAccess
	}
	if req.DAVAccess != nil {
		m.DAVAccess = *req.DAVAccess
	}
	if req.ForcePwUpdate != nil {
		m.ForcePwUpdate = *req.ForcePwUpdate
	}
	if req.ClearRelayhost {
		m.RelayhostID = nil
	} else if req.RelayhostID != nil {
		m.RelayhostID = req.RelayhostID
	}
}

// DeleteMailbox retira el buzon y todo lo que solo tiene sentido con el: contrasenas de
// aplicacion, filtros sieve, respuesta automatica, uso de cuota, permisos de remitente y aliases temporales que
// entregaban en el. El uso de cuota se borra ANTES que el buzon: la politica que lo
// permite exige que el buzon exista.
func (uc *UseCase) DeleteMailbox(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		steps := []func() error{
			func() error { return uc.mailboxes.DeleteQuotaUsage(ctx, tenantID, m.Username) },
			func() error { return uc.appPasswords.DeleteByMailbox(ctx, tenantID, id) },
			func() error { return uc.sieve.DeleteByUsername(ctx, tenantID, m.Username) },
			func() error { return uc.vacation.DeleteByUsername(ctx, tenantID, m.Username) },
			func() error { return uc.senderACL.DeleteByLoggedInAs(ctx, tenantID, m.Username) },
			func() error { return uc.spamAliases.DeleteByGoto(ctx, tenantID, m.Username) },
			func() error { return uc.mailboxes.Delete(ctx, tenantID, id) },
			func() error { return uc.events.MailboxDeleted(ctx, m) },
		}
		for _, step := range steps {
			if err := step(); err != nil {
				return err
			}
		}
		return nil
	})
}

func (uc *UseCase) SetMailboxPassword(ctx context.Context, tenantID, id uuid.UUID, password string) error {
	if err := domain.ValidatePassword(password); err != nil {
		return err
	}
	hash, err := uc.secrets.HashPassword(password)
	if err != nil {
		return err
	}
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if err := uc.mailboxes.UpdatePassword(ctx, tenantID, id, hash); err != nil {
			return err
		}
		return uc.events.MailboxCredentialsChanged(ctx, m, domain.CredentialPassword, []domain.MailboxAttr{domain.AttrPassword})
	})
}

func (uc *UseCase) MailboxQuota(ctx context.Context, tenantID, id uuid.UUID) (q *domain.QuotaUsage, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		q, err = uc.mailboxes.Quota(ctx, tenantID, id)
		return err
	})
	return q, err
}

func (uc *UseCase) MailboxLogins(ctx context.Context, tenantID, id uuid.UUID, limit int) (logins []domain.SASLLogin, err error) {
	if limit < 1 {
		limit = defaultLogins
	}
	if limit > maxLogins {
		limit = maxLogins
	}
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, id)
		if err != nil {
			return err
		}
		logins, err = uc.mailboxes.Logins(ctx, tenantID, m.Username, limit)
		return err
	})
	return logins, err
}

// ── Contrasenas de aplicacion ─────────────────────────────────────────────────

type CreateAppPasswordRequest struct {
	Name        string
	IMAPAccess  *bool
	POP3Access  *bool
	SMTPAccess  *bool
	SieveAccess *bool
	DAVAccess   *bool
}

type UpdateAppPasswordRequest struct {
	Name        *string
	IMAPAccess  *bool
	POP3Access  *bool
	SMTPAccess  *bool
	SieveAccess *bool
	DAVAccess   *bool
	Active      *bool
}

func (r UpdateAppPasswordRequest) empty() bool {
	return r.Name == nil && r.IMAPAccess == nil && r.POP3Access == nil && r.SMTPAccess == nil &&
		r.SieveAccess == nil && r.DAVAccess == nil && r.Active == nil
}

func (uc *UseCase) ListAppPasswords(ctx context.Context, tenantID, mailboxID uuid.UUID) (items []domain.AppPassword, err error) {
	err = uc.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := uc.mailboxes.Get(ctx, tenantID, mailboxID); err != nil {
			return err
		}
		items, err = uc.appPasswords.List(ctx, tenantID, mailboxID)
		return err
	})
	return items, err
}

// CreateAppPassword genera la contrasena en el servidor y la devuelve UNA sola vez; en la
// base solo queda el hash. No se anuncia: una credencial nueva no deja ninguna vieja valiendo.
func (uc *UseCase) CreateAppPassword(ctx context.Context, tenantID, mailboxID uuid.UUID, req CreateAppPasswordRequest) (*domain.AppPassword, string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, "", domain.ErrNameRequired
	}
	plain, err := uc.secrets.GenerateAppPassword()
	if err != nil {
		return nil, "", err
	}
	hash, err := uc.secrets.HashPassword(plain)
	if err != nil {
		return nil, "", err
	}
	p := &domain.AppPassword{
		ID: uuid.New(), TenantID: tenantID, MailboxID: mailboxID, Name: name, PasswordHash: hash,
		IMAPAccess: boolOr(req.IMAPAccess, true), POP3Access: boolOr(req.POP3Access, true),
		SMTPAccess: boolOr(req.SMTPAccess, true), SieveAccess: boolOr(req.SieveAccess, true),
		DAVAccess: boolOr(req.DAVAccess, true), Active: true,
	}
	err = uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		if _, err := uc.mailboxes.Get(ctx, tenantID, mailboxID); err != nil {
			return err
		}
		existing, err := uc.appPasswords.List(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		if len(existing) >= domain.MaxAppPasswordsPerMailbox {
			return domain.ErrMaxAppPasswordsReached
		}
		return uc.appPasswords.Create(ctx, p)
	})
	if err != nil {
		return nil, "", err
	}
	return p, plain, nil
}

// UpdateAppPassword anuncia en su transaccion el cambio que le quita un inicio de sesion
// (domain.AppPasswordLoginsRevoked); el resto no tiene nada que retirar de Dovecot.
func (uc *UseCase) UpdateAppPassword(ctx context.Context, tenantID, mailboxID, id uuid.UUID, req UpdateAppPasswordRequest) (*domain.AppPassword, error) {
	if req.empty() {
		return nil, domain.ErrNothingToUpdate
	}
	var p *domain.AppPassword
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		var err error
		p, err = uc.appPasswords.Get(ctx, tenantID, mailboxID, id)
		if err != nil {
			return err
		}
		before := *p
		if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
			p.Name = strings.TrimSpace(*req.Name)
		}
		p.IMAPAccess = boolOr(req.IMAPAccess, p.IMAPAccess)
		p.POP3Access = boolOr(req.POP3Access, p.POP3Access)
		p.SMTPAccess = boolOr(req.SMTPAccess, p.SMTPAccess)
		p.SieveAccess = boolOr(req.SieveAccess, p.SieveAccess)
		p.DAVAccess = boolOr(req.DAVAccess, p.DAVAccess)
		p.Active = boolOr(req.Active, p.Active)
		if err := uc.appPasswords.Update(ctx, p); err != nil {
			return err
		}
		if !domain.AppPasswordLoginsRevoked(before, p) {
			return nil
		}
		return uc.appPasswordRevoked(ctx, tenantID, mailboxID)
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (uc *UseCase) DeleteAppPassword(ctx context.Context, tenantID, mailboxID, id uuid.UUID) error {
	return uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		p, err := uc.appPasswords.Get(ctx, tenantID, mailboxID, id)
		if err != nil {
			return err
		}
		if err := uc.appPasswords.Delete(ctx, tenantID, mailboxID, id); err != nil {
			return err
		}
		if !domain.AppPasswordLoginsRevoked(*p, nil) {
			return nil
		}
		return uc.appPasswordRevoked(ctx, tenantID, mailboxID)
	})
}

// appPasswordRevoked anuncia que una contrasena de aplicacion del buzon perdio un inicio de
// sesion: mail-security retira de la cache de Dovecot la autenticacion que quedara y cierra las
// sesiones del buzon, que no dicen con que credencial entraron.
func (uc *UseCase) appPasswordRevoked(ctx context.Context, tenantID, mailboxID uuid.UUID) error {
	m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
	if err != nil {
		return err
	}
	return uc.events.MailboxCredentialsChanged(ctx, m, domain.CredentialAppPassword, []domain.MailboxAttr{domain.AttrAppPassword})
}

// ── Sieve ─────────────────────────────────────────────────────────────────────

type SieveScript struct {
	ScriptDesc string
	ScriptData string
	Active     bool
}

// PutSieveRequest reemplaza ambos filtros del buzon; un puntero nil elimina ese filtro.
type PutSieveRequest struct {
	Prefilter  *SieveScript
	Postfilter *SieveScript
}

func (uc *UseCase) GetMailboxSieve(ctx context.Context, tenantID, mailboxID uuid.UUID) (*domain.MailboxSieve, error) {
	var out domain.MailboxSieve
	err := uc.tx.InTx(ctx, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		filters, err := uc.sieve.ByUsername(ctx, tenantID, m.Username)
		if err != nil {
			return err
		}
		for i := range filters {
			f := filters[i]
			switch f.FilterType {
			case domain.SieveTypePrefilter:
				out.Prefilter = &f
			case domain.SieveTypePostfilter:
				out.Postfilter = &f
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (uc *UseCase) PutMailboxSieve(ctx context.Context, tenantID, mailboxID uuid.UUID, req PutSieveRequest) (*domain.MailboxSieve, error) {
	for _, s := range []*SieveScript{req.Prefilter, req.Postfilter} {
		if s != nil {
			if err := domain.ValidateSieveScript(s.ScriptData); err != nil {
				return nil, err
			}
		}
	}
	err := uc.writeTx(ctx, tenantID, func(ctx context.Context) error {
		m, err := uc.mailboxes.Get(ctx, tenantID, mailboxID)
		if err != nil {
			return err
		}
		for filterType, s := range map[string]*SieveScript{
			domain.SieveTypePrefilter:  req.Prefilter,
			domain.SieveTypePostfilter: req.Postfilter,
		} {
			var f *domain.SieveFilter
			if s != nil {
				f = &domain.SieveFilter{
					ID: uuid.New(), TenantID: tenantID, Username: m.Username, FilterType: filterType,
					ScriptDesc: strings.TrimSpace(s.ScriptDesc), ScriptData: s.ScriptData, Active: s.Active,
				}
			}
			if err := uc.sieve.Replace(ctx, tenantID, m.Username, filterType, f); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return uc.GetMailboxSieve(ctx, tenantID, mailboxID)
}
