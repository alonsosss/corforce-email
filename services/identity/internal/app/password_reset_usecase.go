package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const resetTokenTTL = 30 * time.Minute

// productName es la marca que ve el usuario en el correo de recuperacion.
const productName = "Core Force Mail"

// PasswordResetUseCase implementa el flujo self-service "olvide mi contrasena":
// solicitud por email (token de un solo uso enviado por correo) y confirmacion con
// contrasena nueva. Disenado contra enumeracion de cuentas: la solicitud responde
// siempre igual, exista o no el correo.
type PasswordResetUseCase struct {
	users         ports.UserRepository
	resets        ports.PasswordResetRepository
	policies      ports.PasswordPolicyRepository
	breach        ports.PasswordBreachChecker
	hasher        ports.PasswordHasher
	history       ports.PasswordHistoryRepository
	sessions      ports.SessionRepository
	audit         ports.AuditRepository
	events        ports.EventPublisher
	mailer        ports.TransactionalMailer
	tenants       ports.TenantRepository
	logger        *zap.Logger
	publicBaseURL string
}

type PasswordResetDeps struct {
	Users    ports.UserRepository
	Resets   ports.PasswordResetRepository
	Policies ports.PasswordPolicyRepository
	Breach   ports.PasswordBreachChecker
	Hasher   ports.PasswordHasher
	History  ports.PasswordHistoryRepository
	Sessions ports.SessionRepository
	Audit    ports.AuditRepository
	Events   ports.EventPublisher
	Mailer   ports.TransactionalMailer
	Tenants  ports.TenantRepository
	Logger   *zap.Logger
	// PublicBaseURL es el origen publico del frontend donde vive /reset-password. Sin
	// el no hay enlace que enviar: la solicitud se registra y no sale ningun correo.
	PublicBaseURL string
}

func NewPasswordResetUseCase(deps PasswordResetDeps) *PasswordResetUseCase {
	return &PasswordResetUseCase{
		users:         deps.Users,
		resets:        deps.Resets,
		policies:      deps.Policies,
		breach:        deps.Breach,
		hasher:        deps.Hasher,
		history:       deps.History,
		sessions:      deps.Sessions,
		audit:         deps.Audit,
		events:        deps.Events,
		mailer:        deps.Mailer,
		tenants:       deps.Tenants,
		logger:        deps.Logger,
		publicBaseURL: strings.TrimRight(strings.TrimSpace(deps.PublicBaseURL), "/"),
	}
}

// RequestReset genera y envia el enlace de recuperacion. Nunca devuelve error de
// negocio al llamador: si el correo no existe o el envio falla se registra en el
// log y la respuesta al cliente es identica (anti-enumeracion).
func (uc *PasswordResetUseCase) RequestReset(ctx context.Context, email, ipAddress string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if uc.publicBaseURL == "" {
		uc.logger.Error("password reset: PUBLIC_BASE_URL no configurada; no se puede construir el enlace")
		return
	}
	tenantID, err := uc.tenants.GetIDByEmail(ctx, email)
	if err != nil {
		uc.logger.Info("password reset: correo no registrado", zap.String("email", email))
		return
	}
	user, err := uc.users.GetByEmail(ctx, tenantID, email)
	if err != nil || user.Status == domain.UserStatusInactive {
		uc.logger.Info("password reset: usuario no elegible", zap.String("email", email))
		return
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		uc.logger.Error("password reset: generar token", zap.Error(err))
		return
	}
	token := hex.EncodeToString(raw)

	if err := uc.resets.InvalidateForUser(ctx, user.ID); err != nil {
		uc.logger.Warn("password reset: invalidar tokens previos", zap.Error(err))
	}
	if err := uc.resets.Create(ctx, &domain.PasswordResetToken{
		ID:        uuid.New(),
		UserID:    user.ID,
		TenantID:  tenantID,
		TokenHash: hashResetToken(token),
		ExpiresAt: time.Now().Add(resetTokenTTL),
		CreatedAt: time.Now(),
	}); err != nil {
		uc.logger.Error("password reset: guardar token", zap.Error(err))
		return
	}

	resetURL := fmt.Sprintf("%s/reset-password?token=%s", uc.publicBaseURL, url.QueryEscape(token))
	subject := "Restablece tu contrasena de " + productName
	body := resetEmailBody(user.FirstName, resetURL)
	if err := uc.mailer.Send(ctx, tenantID, user.Email, subject, body); err != nil {
		uc.logger.Error("password reset: enviar correo", zap.String("email", email), zap.Error(err))
		return
	}

	uc.audit.Log(ctx, &domain.AuditEntry{
		ID:        uuid.New(),
		TenantID:  tenantID,
		UserID:    user.ID,
		Action:    "password_reset_requested",
		Resource:  "user",
		IPAddress: ipAddress,
		CreatedAt: time.Now(),
	})
}

// RulesForToken devuelve las reglas de la empresa del usuario al que pertenece un enlace
// de reinicio vigente. Es publico: no revela nada del usuario, solo la forma que la
// empresa exige a cualquier contrasena. Un enlace invalido o gastado no devuelve reglas.
func (uc *PasswordResetUseCase) RulesForToken(ctx context.Context, token string) (domain.PasswordRules, error) {
	prt, err := uc.resets.GetByTokenHash(ctx, hashResetToken(token))
	if err != nil || prt.UsedAt != nil || time.Now().After(prt.ExpiresAt) {
		return domain.PasswordRules{}, domain.ErrResetTokenInvalid
	}
	user, err := uc.users.GetByID(ctx, prt.UserID)
	if err != nil {
		return domain.PasswordRules{}, domain.ErrResetTokenInvalid
	}
	policy, err := uc.policies.Get(ctx, user.TenantID)
	if err != nil {
		return domain.PasswordRules{}, fmt.Errorf("password policy: %w", err)
	}
	return policy.Rules(breachEnabled(uc.breach)), nil
}

// ConfirmReset valida el token y fija la contrasena nueva. Ademas desbloquea la
// cuenta y revoca todas las sesiones activas (si alguien tenia acceso a la cuenta
// comprometida, lo pierde en el acto).
func (uc *PasswordResetUseCase) ConfirmReset(ctx context.Context, token, newPassword string) error {
	prt, err := uc.resets.GetByTokenHash(ctx, hashResetToken(token))
	if err != nil || prt.UsedAt != nil || time.Now().After(prt.ExpiresAt) {
		return domain.ErrResetTokenInvalid
	}
	user, err := uc.users.GetByID(ctx, prt.UserID)
	if err != nil {
		return domain.ErrResetTokenInvalid
	}

	policy, err := uc.policies.Get(ctx, user.TenantID)
	if err != nil {
		return fmt.Errorf("password policy: %w", err)
	}
	if err := checkNewPassword(ctx, newPassword, policy, uc.breach, uc.logger); err != nil {
		return err
	}

	hash, err := uc.hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := uc.users.UpdatePassword(ctx, user.ID, hash); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if err := uc.resets.MarkUsed(ctx, prt.ID); err != nil {
		uc.logger.Warn("password reset: marcar token usado", zap.Error(err))
	}
	if err := uc.users.ResetFailedAttempts(ctx, user.ID); err != nil {
		uc.logger.Warn("password reset: reiniciar intentos fallidos", zap.Error(err))
	}
	if err := uc.sessions.RevokeAllByUser(ctx, user.ID); err != nil {
		uc.logger.Warn("password reset: revocar sesiones", zap.Error(err))
	}
	// Los access token ya emitidos tambien caen: el gateway rechaza los anteriores al
	// epoch. Best-effort, igual que en logout-all.
	if err := uc.users.BumpTokenEpoch(ctx, user.ID); err != nil {
		uc.logger.Warn("password reset: adelantar el epoch de tokens", zap.Error(err))
	}

	uc.history.Add(ctx, user.ID, hash)
	uc.events.PublishPasswordChanged(user.TenantID.String(), user.ID.String())
	uc.audit.Log(ctx, &domain.AuditEntry{
		ID:         uuid.New(),
		TenantID:   user.TenantID,
		UserID:     user.ID,
		Action:     "password_reset_completed",
		Resource:   "user",
		ResourceID: user.ID.String(),
		CreatedAt:  time.Now(),
	})
	return nil
}

func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func resetEmailBody(firstName, resetURL string) string {
	greeting := "Hola"
	if strings.TrimSpace(firstName) != "" {
		greeting = "Hola " + strings.TrimSpace(firstName)
	}
	return fmt.Sprintf(`<div style="font-family:system-ui,-apple-system,sans-serif;max-width:520px;margin:0 auto;padding:24px">
<h2 style="font-size:18px;margin:0 0 16px">Restablecer contrasena</h2>
<p style="color:#333;line-height:1.5">%s, recibimos una solicitud para restablecer la contrasena de tu cuenta en %s.</p>
<p style="margin:24px 0"><a href="%s" style="background:#111;color:#fff;padding:12px 24px;border-radius:8px;text-decoration:none;display:inline-block">Crear contrasena nueva</a></p>
<p style="color:#666;font-size:13px;line-height:1.5">El enlace vence en 30 minutos y solo puede usarse una vez. Si no solicitaste este cambio, ignora este correo: tu contrasena actual sigue vigente.</p>
<p style="color:#999;font-size:12px;margin-top:24px">Si el boton no funciona, copia y pega esta direccion en tu navegador:<br>%s</p>
</div>`, greeting, productName, resetURL, resetURL)
}
