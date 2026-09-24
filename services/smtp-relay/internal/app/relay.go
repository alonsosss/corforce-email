// Package app es el caso de uso del relay: admitir una conexion, autenticar la credencial SMTP de
// una empresa, aceptar el sobre y entregar el mensaje a transactional, que aplica las mismas
// reglas que su API (remitente verificado, supresion, reputacion, facturacion).
package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/domain"
	"github.com/alonsosss/corforce-email/services/smtp-relay/internal/ports"
	"go.uber.org/zap"
)

// Config son los limites de una sesion, ya validados en main.
type Config struct {
	MaxRecipients   int
	MaxMessageBytes int
}

type Deps struct {
	Auth      ports.Authenticator
	Throttle  ports.Throttle
	Limits    ports.Limits
	Inspector ports.Inspector
	// Scanner es nil sin clamd configurado (solo en desarrollo y prueba): entonces un mensaje con
	// adjuntos no se acepta.
	Scanner   ports.Scanner
	Submitter ports.Submitter
	Metrics   ports.Metrics
	Config    Config
	Logger    *zap.Logger
	Now       func() time.Time
}

type Relay struct {
	d Deps
}

func New(d Deps) *Relay {
	if d.Logger == nil {
		d.Logger = zap.NewNop()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Relay{d: d}
}

// Etapas de una sesion para las metricas de rechazo.
const (
	StageConnect = "connect"
	StageAuth    = "auth"
	StageMail    = "mail"
	StageRcpt    = "rcpt"
	StageData    = "data"
)

func (r *Relay) rejected(stage string, err error) error {
	r.d.Metrics.Rejected(stage, domain.AsRejection(err).Reason)
	return err
}

// Admit decide si una conexion nueva cabe en el cupo de su IP.
func (r *Relay) Admit(ctx context.Context, listener, ip string) error {
	r.d.Metrics.Connection(listener)
	if !r.d.Limits.AllowConnection(ctx, ip) {
		return r.rejected(StageConnect, domain.ErrConnectionLimit)
	}
	return nil
}

// Authenticate comprueba la credencial SMTP: el usuario es el prefijo de la clave y la contrasena
// la clave entera. Un fallo cuenta en el freno; una caida de access-control no, porque no dice
// nada de quien prueba.
func (r *Relay) Authenticate(ctx context.Context, username, password, ip string) (*domain.Credential, error) {
	username = strings.TrimSpace(username)
	if r.d.Throttle.Blocked(ctx, username, ip) {
		r.d.Metrics.Auth("blocked")
		return nil, r.rejected(StageAuth, domain.ErrBlocked)
	}
	if username == "" || !strings.HasPrefix(password, domain.TokenPrefix+username+"_") {
		return nil, r.authFailed(ctx, username, ip)
	}
	cred, err := r.d.Auth.Authenticate(ctx, password, ip)
	switch {
	case errors.Is(err, domain.ErrAuthFailed):
		return nil, r.authFailed(ctx, username, ip)
	case err != nil:
		r.d.Metrics.Auth("unavailable")
		r.d.Logger.Warn("smtp-relay: no se pudo comprobar una credencial", zap.Error(err))
		return nil, r.rejected(StageAuth, domain.ErrAuthUnavailable)
	}
	r.d.Throttle.Success(ctx, username, ip)
	r.d.Metrics.Auth("ok")
	return cred, nil
}

func (r *Relay) authFailed(ctx context.Context, username, ip string) error {
	r.d.Throttle.Failure(ctx, username, ip)
	r.d.Metrics.Auth("failed")
	r.d.Logger.Info("smtp-relay: credencial rechazada", zap.String("username", username), zap.String("remote_ip", ip))
	return r.rejected(StageAuth, domain.ErrAuthFailed)
}

// Mail abre un mensaje: exige sesion autenticada, vuelve a comprobar la clave (una sesion larga no
// sobrevive a su revocacion, a su caducidad ni a que su creador pierda el permiso) y exige cupo de
// mensajes para la clave y la IP.
func (r *Relay) Mail(ctx context.Context, cred *domain.Credential, ip, from string) (string, error) {
	if cred == nil {
		return "", r.rejected(StageMail, domain.ErrAuthRequired)
	}
	current, err := r.d.Auth.Authenticate(ctx, cred.Token, ip)
	switch {
	case errors.Is(err, domain.ErrAuthFailed):
		return "", r.rejected(StageMail, domain.ErrKeyRevoked)
	case err != nil:
		return "", r.rejected(StageMail, domain.ErrAuthUnavailable)
	case current.KeyID != cred.KeyID || current.TenantID != cred.TenantID:
		return "", r.rejected(StageMail, domain.ErrKeyRevoked)
	}
	addr, ok := domain.NormalizeAddress(from)
	if !ok {
		return "", r.rejected(StageMail, domain.ErrInvalidAddress)
	}
	if !r.d.Limits.AllowMessage(ctx, cred.KeyID, ip) {
		return "", r.rejected(StageMail, domain.ErrMessageRate)
	}
	return addr, nil
}

// Rcpt anade un destinatario al sobre. Uno repetido se acepta sin duplicarlo.
func (r *Relay) Rcpt(current []string, to string) ([]string, error) {
	addr, ok := domain.NormalizeAddress(to)
	if !ok {
		return current, r.rejected(StageRcpt, domain.ErrInvalidAddress)
	}
	for _, c := range current {
		if strings.EqualFold(c, addr) {
			return current, nil
		}
	}
	if len(current) >= r.d.Config.MaxRecipients {
		return current, r.rejected(StageRcpt, domain.ErrTooManyRecipients)
	}
	return append(current, addr), nil
}

// Deliver lee el mensaje, analiza sus adjuntos y lo entrega a transactional. Devuelve el id del
// mensaje creado.
func (r *Relay) Deliver(ctx context.Context, cred *domain.Credential, env domain.Envelope, raw []byte) (string, error) {
	start := r.d.Now()
	if cred == nil {
		return "", r.rejected(StageData, domain.ErrAuthRequired)
	}
	if len(env.Recipients) == 0 || env.From == "" {
		return "", r.rejected(StageData, domain.ErrInvalidAddress)
	}
	if len(raw) > r.d.Config.MaxMessageBytes {
		return "", r.rejected(StageData, domain.ErrTooLarge)
	}
	inspection, err := r.d.Inspector.Inspect(raw)
	if err != nil {
		return "", r.rejected(StageData, err)
	}
	if inspection.HasAttachments {
		if r.d.Scanner == nil {
			return "", r.rejected(StageData, domain.ErrScanUnavailable)
		}
		if err := r.d.Scanner.Scan(ctx, raw); err != nil {
			if errors.Is(err, domain.ErrInfected) {
				r.d.Logger.Warn("smtp-relay: adjunto infectado rechazado",
					zap.String("tenant_id", cred.TenantID), zap.String("api_key_id", cred.KeyID), zap.Error(err))
			}
			return "", r.rejected(StageData, err)
		}
	}
	id, err := r.d.Submitter.Submit(ctx, *cred, env, raw, domain.IdempotencyKey(cred.TenantID, inspection.MessageID, env))
	if err != nil {
		return "", r.rejected(StageData, err)
	}
	r.d.Metrics.Delivered(len(raw), r.d.Now().Sub(start))
	return id, nil
}
