package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"regexp"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

const (
	tokenBytes = 32
	// maxPasswordBytes acota lo que se reenvia a mail-auth; bcrypt solo mira 72 bytes.
	maxPasswordBytes = 1024
)

// tokenPattern es un token de 256 bits en base64url sin relleno.
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// Login verifica la credencial contra mail-auth y abre una sesion nueva.
//
// previousToken es la cookie que el navegador ya traia: se destruye para que un inicio
// de sesion rote SIEMPRE el token (una cookie fijada por un tercero antes del login no
// sobrevive a el). Cualquier rechazo es domain.ErrInvalidCredentials sin distinguir la
// causa; el freno de fuerza bruta es el de mail-auth, que recibe la IP real del cliente.
func (s *Service) Login(ctx context.Context, rawUsername, password, remoteIP, previousToken string) (string, domain.Session, error) {
	username, ok := domain.NormalizeUsername(rawUsername)
	if !ok || password == "" || len(password) > maxPasswordBytes {
		return "", domain.Session{}, domain.ErrInvalidCredentials
	}
	started := s.clock()
	id, err := s.auth.Verify(ctx, username, password, remoteIP)
	if err != nil {
		if !errors.Is(err, domain.ErrInvalidCredentials) {
			s.logger.Error("webmail: no se pudo verificar la credencial", zap.String("remote_ip", remoteIP), zap.Error(err))
			return "", domain.Session{}, unavailable(err)
		}
		s.logger.Info("webmail: inicio de sesion rechazado", zap.String("username", username), zap.String("remote_ip", remoteIP))
		return "", domain.Session{}, domain.ErrInvalidCredentials
	}
	id.Username = username

	s.discard(ctx, previousToken)

	token, err := newToken()
	if err != nil {
		return "", domain.Session{}, unavailable(err)
	}
	key, _ := sessionKey(token)
	sess := s.cfg.Sessions.Open(id, started)
	ttl, alive := s.cfg.Sessions.Remaining(sess, s.clock())
	if !alive {
		return "", domain.Session{}, unavailable(errors.New("la verificacion supero la vida maxima de la sesion"))
	}
	if err := s.sessions.Create(ctx, key, sess, ttl); err != nil {
		s.logger.Error("webmail: no se pudo guardar la sesion", zap.String("username", username), zap.Error(err))
		return "", domain.Session{}, unavailable(err)
	}
	s.logger.Info("webmail: inicio de sesion", zap.String("username", username),
		zap.String("remote_ip", remoteIP), zap.String("session", key[:12]))
	return token, sess, nil
}

// Authenticate resuelve la sesion de un token y renueva su inactividad. Una sesion
// caducada o revocada se borra y se rechaza. Si el almacen no responde se falla cerrado:
// sin poder comprobar la revocacion no se sirve el buzon.
func (s *Service) Authenticate(ctx context.Context, token string) (domain.Session, error) {
	key, ok := sessionKey(token)
	if !ok {
		return domain.Session{}, domain.ErrSessionInvalid
	}
	sess, err := s.sessions.Get(ctx, key)
	if err != nil {
		if errors.Is(err, domain.ErrSessionInvalid) {
			return domain.Session{}, err
		}
		return domain.Session{}, unavailable(err)
	}
	ttl, alive := s.cfg.Sessions.Remaining(sess, s.clock())
	if !alive {
		s.drop(ctx, key, sess.Username, "vida maxima")
		return domain.Session{}, domain.ErrSessionInvalid
	}
	revokedAt, err := s.sessions.RevokedAt(ctx, sess.Username)
	if err != nil {
		return domain.Session{}, unavailable(err)
	}
	if sess.RevokedBy(revokedAt) {
		s.drop(ctx, key, sess.Username, "revocada")
		return domain.Session{}, domain.ErrSessionInvalid
	}
	if err := s.sessions.Touch(ctx, key, ttl); err != nil {
		// La sesion es valida: si no se pudo renovar, caducara antes, que es el lado seguro.
		s.logger.Warn("webmail: no se pudo renovar la inactividad de la sesion", zap.Error(err))
	}
	return sess, nil
}

// Logout destruye la sesion del token. Es idempotente.
func (s *Service) Logout(ctx context.Context, token string) error {
	key, ok := sessionKey(token)
	if !ok {
		return nil
	}
	sess, err := s.sessions.Get(ctx, key)
	if errors.Is(err, domain.ErrSessionInvalid) {
		return nil
	}
	if err != nil {
		return unavailable(err)
	}
	if err := s.sessions.Delete(ctx, key, sess.Username); err != nil {
		return unavailable(err)
	}
	s.logger.Info("webmail: cierre de sesion", zap.String("username", sess.Username), zap.String("session", key[:12]))
	return nil
}

// RevokeMailbox invalida todas las sesiones del buzon abiertas hasta at: cambio de
// contrasena, desactivacion, baja o cualquier cambio administrativo del buzon.
func (s *Service) RevokeMailbox(ctx context.Context, username string, at time.Time) error {
	if err := s.sessions.Revoke(ctx, username, at); err != nil {
		return unavailable(err)
	}
	s.logger.Info("webmail: sesiones del buzon revocadas", zap.String("username", username), zap.Time("revoked_at", at))
	return nil
}

// discard borra la sesion de un token previo sin fallar el inicio si no puede.
func (s *Service) discard(ctx context.Context, token string) {
	if token == "" {
		return
	}
	key, ok := sessionKey(token)
	if !ok {
		return
	}
	prev, err := s.sessions.Get(ctx, key)
	if err != nil {
		return
	}
	s.drop(ctx, key, prev.Username, "rotacion en el inicio de sesion")
}

func (s *Service) drop(ctx context.Context, key, username, reason string) {
	if err := s.sessions.Delete(ctx, key, username); err != nil {
		s.logger.Warn("webmail: no se pudo borrar la sesion", zap.String("reason", reason), zap.Error(err))
	}
}

// sessionKey es la clave de almacen de un token: su SHA-256. Un volcado de Redis no
// entrega tokens utilizables.
func sessionKey(token string) (string, bool) {
	if !tokenPattern.MatchString(token) {
		return "", false
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]), true
}

func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
