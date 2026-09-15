package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"
)

// PolicyUseCase es el API de administracion de politicas. Toda lectura y escritura va
// dentro de TransactRLS y acotada por la empresa de la peticion; las claves de Redis se
// escriben DESPUES de confirmar, y si Redis falla se registra: la reconciliacion
// periodica las vuelve a poner.
type PolicyUseCase struct {
	tx     ports.Transactor
	repo   ports.PolicyRepository
	dir    ports.DirectoryReader
	sync   *RedisSync
	logger *zap.Logger
}

type PolicyDeps struct {
	Tx        ports.Transactor
	Repo      ports.PolicyRepository
	Directory ports.DirectoryReader
	Sync      *RedisSync
	Logger    *zap.Logger
}

func NewPolicyUseCase(d PolicyDeps) *PolicyUseCase {
	return &PolicyUseCase{tx: d.Tx, repo: d.Repo, dir: d.Directory, sync: d.Sync, logger: d.Logger}
}

func (uc *PolicyUseCase) afterCommit(ctx context.Context, what string, fn func(context.Context) error) {
	logAfterCommit(ctx, uc.logger, what, fn)
}

// logAfterCommit escribe Redis tras confirmar; un fallo solo se registra porque la
// reconciliacion periodica vuelve a poner la clave desde la base.
func logAfterCommit(ctx context.Context, logger *zap.Logger, what string, fn func(context.Context) error) {
	if err := fn(ctx); err != nil {
		logger.Error("redis no actualizado; la reconciliacion lo corregira", zap.String("clave", what), zap.Error(err))
	}
}

// ownedObject valida el formato del objeto y que pertenezca a la empresa.
func (uc *PolicyUseCase) ownedObject(ctx context.Context, tenantID uuid.UUID, object string) (string, error) {
	object = strings.ToLower(strings.TrimSpace(object))
	if err := domain.ValidateObject(object); err != nil {
		return "", err
	}
	owned, err := uc.dir.ObjectOwnedBy(ctx, tenantID, object)
	if err != nil {
		return "", err
	}
	if !owned {
		return "", domain.ErrObjectNotOwned
	}
	return object, nil
}

// ── Umbrales ─────────────────────────────────────────────────────────────────

func (uc *PolicyUseCase) ListSpamScores(ctx context.Context, tenantID uuid.UUID) (out []domain.SpamScore, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.ListSpamScores(ctx, tenantID)
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) GetSpamScore(ctx context.Context, tenantID uuid.UUID, object string) (out *domain.SpamScore, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.GetSpamScore(ctx, tenantID, strings.ToLower(object))
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) PutSpamScore(ctx context.Context, tenantID uuid.UUID, object string, high, low decimal.Decimal) (out *domain.SpamScore, err error) {
	if low.GreaterThan(high) {
		return nil, &domain.ValidationError{Msg: "low_score no puede superar high_score"}
	}
	if high.LessThanOrEqual(decimal.Zero) {
		return nil, &domain.ValidationError{Msg: "high_score debe ser mayor que cero"}
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		obj, err := uc.ownedObject(ctx, tenantID, object)
		if err != nil {
			return err
		}
		s := &domain.SpamScore{TenantID: tenantID, Object: obj, HighScore: high, LowScore: low}
		if err := uc.repo.UpsertSpamScore(ctx, s); err != nil {
			return err
		}
		out = s
		return nil
	})
	return out, err
}

func (uc *PolicyUseCase) DeleteSpamScore(ctx context.Context, tenantID uuid.UUID, object string) error {
	return uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		return uc.repo.DeleteSpamScore(ctx, tenantID, strings.ToLower(object))
	})
}

// ── Listas ────────────────────────────────────────────────────────────────────

func (uc *PolicyUseCase) ListAddressLists(ctx context.Context, tenantID uuid.UUID, object string, kind domain.ListKind) (out []domain.AddressListEntry, err error) {
	if kind != "" && kind != domain.ListAllow && kind != domain.ListDeny {
		return nil, &domain.ValidationError{Msg: "kind debe ser allow o deny"}
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.ListAddressLists(ctx, tenantID, strings.ToLower(object), kind)
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) CreateAddressList(ctx context.Context, tenantID uuid.UUID, object string, kind domain.ListKind, pattern string) (out *domain.AddressListEntry, err error) {
	if kind != domain.ListAllow && kind != domain.ListDeny {
		return nil, &domain.ValidationError{Msg: "kind debe ser allow o deny"}
	}
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if err := domain.ValidateListPattern(pattern); err != nil {
		return nil, err
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		obj, err := uc.ownedObject(ctx, tenantID, object)
		if err != nil {
			return err
		}
		e := &domain.AddressListEntry{TenantID: tenantID, Object: obj, Kind: kind, Pattern: pattern}
		if err := uc.repo.CreateAddressList(ctx, e); err != nil {
			return err
		}
		out = e
		return nil
	})
	return out, err
}

func (uc *PolicyUseCase) DeleteAddressList(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		return uc.repo.DeleteAddressList(ctx, tenantID, id)
	})
}

// ── Bloques adicionales ───────────────────────────────────────────────────────

func (uc *PolicyUseCase) ListSettingsMaps(ctx context.Context, tenantID uuid.UUID) (out []domain.SettingsMap, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.ListSettingsMaps(ctx, tenantID)
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) CreateSettingsMap(ctx context.Context, tenantID uuid.UUID, description, content string, active bool) (out *domain.SettingsMap, err error) {
	if err := domain.ValidateSettingsMapContent(content); err != nil {
		return nil, err
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		m := &domain.SettingsMap{TenantID: tenantID, Description: strings.TrimSpace(description), Content: content, Active: active}
		if err := uc.repo.CreateSettingsMap(ctx, m); err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

// SettingsMapPatch lleva solo los campos que el cliente quiere cambiar.
type SettingsMapPatch struct {
	Description *string
	Content     *string
	Active      *bool
}

func (uc *PolicyUseCase) PatchSettingsMap(ctx context.Context, tenantID, id uuid.UUID, p SettingsMapPatch) (out *domain.SettingsMap, err error) {
	if p.Content != nil {
		if err := domain.ValidateSettingsMapContent(*p.Content); err != nil {
			return nil, err
		}
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		m, err := uc.repo.GetSettingsMap(ctx, tenantID, id)
		if err != nil {
			return err
		}
		if p.Description != nil {
			m.Description = strings.TrimSpace(*p.Description)
		}
		if p.Content != nil {
			m.Content = *p.Content
		}
		if p.Active != nil {
			m.Active = *p.Active
		}
		if err := uc.repo.UpdateSettingsMap(ctx, m); err != nil {
			return err
		}
		out = m
		return nil
	})
	return out, err
}

func (uc *PolicyUseCase) DeleteSettingsMap(ctx context.Context, tenantID, id uuid.UUID) error {
	return uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		return uc.repo.DeleteSettingsMap(ctx, tenantID, id)
	})
}

// ── Pies de pagina ────────────────────────────────────────────────────────────

func (uc *PolicyUseCase) ListFooters(ctx context.Context, tenantID uuid.UUID) (out []domain.DomainFooter, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.ListFooters(ctx, tenantID)
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) GetFooter(ctx context.Context, tenantID uuid.UUID, domainName string) (out *domain.DomainFooter, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.GetFooter(ctx, tenantID, strings.ToLower(domainName))
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) PutFooter(ctx context.Context, tenantID uuid.UUID, f domain.DomainFooter) (out *domain.DomainFooter, err error) {
	f.Domain = strings.ToLower(strings.TrimSpace(f.Domain))
	if err := domain.ValidateDomainName(f.Domain); err != nil {
		return nil, err
	}
	if strings.TrimSpace(f.HTML) == "" && strings.TrimSpace(f.Plain) == "" {
		return nil, &domain.ValidationError{Msg: "html o plain deben tener contenido"}
	}
	f.MailboxExclude = lowerAll(f.MailboxExclude)
	f.AliasDomainExclude = lowerAll(f.AliasDomainExclude)
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		if _, err := uc.ownedObject(ctx, tenantID, f.Domain); err != nil {
			return err
		}
		f.TenantID = tenantID
		if err := uc.repo.UpsertFooter(ctx, &f); err != nil {
			return err
		}
		out = &f
		return nil
	})
	return out, err
}

func (uc *PolicyUseCase) DeleteFooter(ctx context.Context, tenantID uuid.UUID, domainName string) error {
	return uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		return uc.repo.DeleteFooter(ctx, tenantID, strings.ToLower(domainName))
	})
}

// ── Hosts de reenvio ──────────────────────────────────────────────────────────

func (uc *PolicyUseCase) ListForwardingHosts(ctx context.Context, tenantID uuid.UUID) (out []domain.ForwardingHost, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.ListForwardingHosts(ctx, tenantID)
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) CreateForwardingHost(ctx context.Context, tenantID uuid.UUID, host, source string, filterSpam bool) (out *domain.ForwardingHost, err error) {
	cidr, err := domain.NormalizeHost(host)
	if err != nil {
		return nil, err
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		h := &domain.ForwardingHost{TenantID: tenantID, Host: cidr, Source: strings.TrimSpace(source), FilterSpam: filterSpam}
		if err := uc.repo.CreateForwardingHost(ctx, h); err != nil {
			return err
		}
		out = h
		return nil
	})
	if err != nil {
		return nil, err
	}
	uc.afterCommit(ctx, domain.RedisWhitelistedFwdHost, func(ctx context.Context) error { return uc.sync.SyncForwardingHost(ctx, *out) })
	return out, nil
}

func (uc *PolicyUseCase) DeleteForwardingHost(ctx context.Context, tenantID, id uuid.UUID) error {
	var host string
	err := uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		h, err := uc.repo.GetForwardingHost(ctx, tenantID, id)
		if err != nil {
			return err
		}
		host = h.Host
		return uc.repo.DeleteForwardingHost(ctx, tenantID, id)
	})
	if err != nil {
		return err
	}
	uc.afterCommit(ctx, domain.RedisWhitelistedFwdHost, func(ctx context.Context) error { return uc.sync.RemoveForwardingHost(ctx, host) })
	return nil
}

// ── Limites de envio ──────────────────────────────────────────────────────────

func (uc *PolicyUseCase) ListRateLimits(ctx context.Context, tenantID uuid.UUID) (out []domain.RateLimit, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.ListRateLimits(ctx, tenantID)
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) GetRateLimit(ctx context.Context, tenantID uuid.UUID, object string) (out *domain.RateLimit, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.GetRateLimit(ctx, tenantID, strings.ToLower(object))
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) PutRateLimit(ctx context.Context, tenantID uuid.UUID, object, value string) (out *domain.RateLimit, err error) {
	value = strings.TrimSpace(value)
	if err := domain.ValidateRateLimitValue(value); err != nil {
		return nil, err
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		obj, err := uc.ownedObject(ctx, tenantID, object)
		if err != nil {
			return err
		}
		r := &domain.RateLimit{TenantID: tenantID, Object: obj, Value: value}
		if err := uc.repo.UpsertRateLimit(ctx, r); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	uc.afterCommit(ctx, domain.RedisRateLimitValue, func(ctx context.Context) error { return uc.sync.SyncRateLimit(ctx, *out) })
	return out, nil
}

func (uc *PolicyUseCase) DeleteRateLimit(ctx context.Context, tenantID uuid.UUID, object string) error {
	object = strings.ToLower(object)
	err := uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		return uc.repo.DeleteRateLimit(ctx, tenantID, object)
	})
	if err != nil {
		return err
	}
	uc.afterCommit(ctx, domain.RedisRateLimitValue, func(ctx context.Context) error { return uc.sync.RemoveRateLimit(ctx, object) })
	return nil
}

// ── Etiquetas por buzon ───────────────────────────────────────────────────────

func (uc *PolicyUseCase) ListMailboxTags(ctx context.Context, tenantID uuid.UUID) (out []domain.MailboxTags, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.ListMailboxTags(ctx, tenantID)
		return err
	})
	return out, err
}

// GetMailboxTags devuelve la fila o, si no existe pero el buzon es de la empresa, los
// valores por defecto (sin etiqueta).
func (uc *PolicyUseCase) GetMailboxTags(ctx context.Context, tenantID uuid.UUID, username string) (out *domain.MailboxTags, err error) {
	username = strings.ToLower(username)
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.GetMailboxTags(ctx, tenantID, username)
		if err != domain.ErrNotFound {
			return err
		}
		owned, err := uc.dir.ObjectOwnedBy(ctx, tenantID, username)
		if err != nil {
			return err
		}
		if !owned || domain.ObjectKindOf(username) != domain.ObjectMailbox {
			return domain.ErrNotFound
		}
		out = &domain.MailboxTags{TenantID: tenantID, Username: username}
		return nil
	})
	return out, err
}

func (uc *PolicyUseCase) PutMailboxTags(ctx context.Context, tenantID uuid.UUID, username string, subjectTag, subfolderTag bool) (out *domain.MailboxTags, err error) {
	if domain.ObjectKindOf(username) != domain.ObjectMailbox {
		return nil, &domain.ValidationError{Msg: "username debe ser un buzon"}
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		user, err := uc.ownedObject(ctx, tenantID, username)
		if err != nil {
			return err
		}
		t := &domain.MailboxTags{TenantID: tenantID, Username: user, SubjectTag: subjectTag, SubfolderTag: subfolderTag}
		if err := uc.repo.UpsertMailboxTags(ctx, t); err != nil {
			return err
		}
		out = t
		return nil
	})
	if err != nil {
		return nil, err
	}
	uc.afterCommit(ctx, domain.RedisWantsSubjectTag, func(ctx context.Context) error { return uc.sync.SyncMailboxTags(ctx, *out) })
	return out, nil
}

// ── Redes SMTP por buzon ──────────────────────────────────────────────────────

func (uc *PolicyUseCase) ListSMTPAccess(ctx context.Context, tenantID uuid.UUID) (out []domain.SMTPAccess, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.ListSMTPAccess(ctx, tenantID)
		return err
	})
	return out, err
}

// GetSMTPAccess devuelve las redes del buzon o, si no tiene y el buzon es de la empresa,
// una lista vacia: sin restriccion.
func (uc *PolicyUseCase) GetSMTPAccess(ctx context.Context, tenantID uuid.UUID, username string) (out *domain.SMTPAccess, err error) {
	username = strings.ToLower(strings.TrimSpace(username))
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.GetSMTPAccess(ctx, tenantID, username)
		if err != domain.ErrNotFound {
			return err
		}
		owned, err := uc.dir.ObjectOwnedBy(ctx, tenantID, username)
		if err != nil {
			return err
		}
		if !owned || domain.ObjectKindOf(username) != domain.ObjectMailbox {
			return domain.ErrNotFound
		}
		out = &domain.SMTPAccess{TenantID: tenantID, Username: username, Networks: []string{}}
		return nil
	})
	return out, err
}

// PutSMTPAccess deja al buzon enviar por SMTP autenticado solo desde esas redes; Rspamd
// puntua 999 cualquier otro origen (SMTP_ACCESS).
func (uc *PolicyUseCase) PutSMTPAccess(ctx context.Context, tenantID uuid.UUID, username string, networks []string) (out *domain.SMTPAccess, err error) {
	if domain.ObjectKindOf(username) != domain.ObjectMailbox {
		return nil, &domain.ValidationError{Msg: "username debe ser un buzon"}
	}
	prefixes, err := domain.NormalizeSMTPNetworks(networks)
	if err != nil {
		return nil, err
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		user, err := uc.ownedObject(ctx, tenantID, username)
		if err != nil {
			return err
		}
		if err := uc.repo.ReplaceSMTPAccess(ctx, tenantID, user, prefixes); err != nil {
			return err
		}
		out, err = uc.repo.GetSMTPAccess(ctx, tenantID, user)
		return err
	})
	if err != nil {
		return nil, err
	}
	uc.afterCommit(ctx, domain.RedisSMTPLimitedAccess, func(ctx context.Context) error { return uc.sync.SyncSMTPAccess(ctx, *out) })
	return out, nil
}

// DeleteSMTPAccess quita la restriccion: el buzon vuelve a enviar desde cualquier red.
func (uc *PolicyUseCase) DeleteSMTPAccess(ctx context.Context, tenantID uuid.UUID, username string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	err := uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		return uc.repo.DeleteSMTPAccess(ctx, tenantID, username)
	})
	if err != nil {
		return err
	}
	uc.afterCommit(ctx, domain.RedisSMTPLimitedAccess, func(ctx context.Context) error { return uc.sync.RemoveSMTPAccess(ctx, username) })
	return nil
}

// ── Ajustes de cuarentena ─────────────────────────────────────────────────────

func (uc *PolicyUseCase) GetQuarantineSettings(ctx context.Context, tenantID uuid.UUID) (out *domain.QuarantineSettings, err error) {
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		out, err = uc.repo.GetQuarantineSettings(ctx, tenantID)
		if err == domain.ErrNotFound {
			def := domain.DefaultQuarantineSettings(tenantID)
			out, err = &def, nil
		}
		return err
	})
	return out, err
}

func (uc *PolicyUseCase) PutQuarantineSettings(ctx context.Context, tenantID uuid.UUID, s domain.QuarantineSettings) (out *domain.QuarantineSettings, err error) {
	if s.MaxSizeBytes <= 0 {
		return nil, &domain.ValidationError{Msg: "max_size_bytes debe ser mayor que cero"}
	}
	if s.MaxAgeDays <= 0 {
		return nil, &domain.ValidationError{Msg: "max_age_days debe ser mayor que cero"}
	}
	if s.RetentionSize < 0 {
		return nil, &domain.ValidationError{Msg: "retention_size no puede ser negativo"}
	}
	s.ExcludeDomains = lowerAll(s.ExcludeDomains)
	for _, d := range s.ExcludeDomains {
		if err := domain.ValidateDomainName(d); err != nil {
			return nil, &domain.ValidationError{Msg: fmt.Sprintf("exclude_domains: %q no es un dominio", d)}
		}
	}
	if s.Notify.MaxScore.IsZero() {
		s.Notify.MaxScore = decimal.NewFromInt(9999)
	}
	s.Notify.Sender, s.Notify.Subject = strings.TrimSpace(s.Notify.Sender), strings.TrimSpace(s.Notify.Subject)
	if s.Notify.Enabled {
		if err := domain.ValidateQuarantineNotify(s.Notify); err != nil {
			return nil, err
		}
	}
	err = uc.tx.TransactRLS(ctx, func(ctx context.Context) error {
		s.TenantID = tenantID
		if err := uc.repo.UpsertQuarantineSettings(ctx, &s); err != nil {
			return err
		}
		out = &s
		return nil
	})
	if err != nil {
		return nil, err
	}
	uc.afterCommit(ctx, domain.RedisQuarantineMaxSize, uc.sync.SyncQuarantineTop)
	return out, nil
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}
