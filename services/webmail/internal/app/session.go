package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

const (
	tokenBytes = 32
	// maxPasswordBytes acota lo que se reenvia a mail-auth; bcrypt solo mira 72 bytes.
	maxPasswordBytes = 1024
)

// LoginResult es el primer paso del inicio de sesion: una sesion abierta (Token y Session) o, con la
// verificacion en dos pasos activa, el token del desafio que espera el codigo (MFAChallenge).
type LoginResult struct {
	Token        string
	Session      domain.Session
	MFAChallenge string
}

// Login verifica la credencial contra mail-auth y abre una sesion nueva, con un token que lleva
// la celda de esta instancia (domain.NewSessionToken). Si el buzon tiene la verificacion en dos
// pasos activa no abre nada: deja un desafio de un solo uso y lo completa CompleteLogin.
//
// previousToken es la cookie que el navegador ya traia: se destruye para que un inicio
// de sesion rote SIEMPRE el token (una cookie fijada por un tercero antes del login no
// sobrevive a el). Cualquier rechazo es domain.ErrInvalidCredentials sin distinguir la
// causa; el freno de fuerza bruta es el de mail-auth, que recibe la IP real del cliente.
func (s *Service) Login(ctx context.Context, rawUsername, password, remoteIP, previousToken string) (LoginResult, error) {
	username, ok := domain.NormalizeUsername(rawUsername)
	if !ok || password == "" || len(password) > maxPasswordBytes {
		return LoginResult{}, domain.ErrInvalidCredentials
	}
	started := s.clock()
	id, err := s.auth.Verify(ctx, username, password, remoteIP)
	if err != nil {
		if !errors.Is(err, domain.ErrInvalidCredentials) {
			s.logger.Error("webmail: no se pudo verificar la credencial", zap.String("remote_ip", remoteIP), zap.Error(err))
			return LoginResult{}, unavailable(err)
		}
		s.logger.Info("webmail: inicio de sesion rechazado", zap.String("username", username), zap.String("remote_ip", remoteIP))
		return LoginResult{}, domain.ErrInvalidCredentials
	}
	id.Username = username
	if id.MFARequired {
		challenge, err := s.startChallenge(ctx, id, started)
		if err != nil {
			return LoginResult{}, err
		}
		s.logger.Info("webmail: contrasena correcta, falta el segundo paso", zap.String("username", username), zap.String("remote_ip", remoteIP))
		return LoginResult{MFAChallenge: challenge}, nil
	}
	token, sess, err := s.openSession(ctx, id, started, remoteIP, previousToken)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, Session: sess}, nil
}

// CompleteLogin es el segundo paso: valida el codigo (TOTP o de recuperacion) con mail-directory y
// abre la sesion igual que Login sin verificacion en dos pasos.
//
// Cada intento se cuenta ANTES de validar, de forma atomica: peticiones en paralelo no pueden
// probar mas codigos que MFAMaxAttempts. El intento que agota el cupo borra el desafio y lo que
// llegue despues es domain.ErrMFAChallengeExpired, como un desafio caducado o inexistente.
func (s *Service) CompleteLogin(ctx context.Context, challengeToken, rawCode, remoteIP, previousToken string) (string, domain.Session, error) {
	key, ok := s.sessionKey(challengeToken)
	if !ok {
		return "", domain.Session{}, domain.ErrMFAChallengeExpired
	}
	ch, err := s.mfaChallenges.GetChallenge(ctx, key)
	if err != nil {
		return "", domain.Session{}, challengeError(err)
	}
	username := ch.Identity.Username
	attempts, err := s.mfaChallenges.CountAttempt(ctx, key)
	if err != nil {
		return "", domain.Session{}, challengeError(err)
	}
	if attempts > s.cfg.MFAMaxAttempts {
		s.dropChallenge(ctx, key)
		return "", domain.Session{}, domain.ErrMFAChallengeExpired
	}
	var verr error
	if code, valid := domain.NormalizeMFACode(rawCode); valid {
		_, verr = s.security.VerifyMFA(ctx, username, code)
	} else {
		verr = domain.ErrInvalidMFACode
	}
	switch {
	case verr == nil:
	case errors.Is(verr, domain.ErrInvalidMFACode):
		if attempts >= s.cfg.MFAMaxAttempts {
			s.dropChallenge(ctx, key)
		}
		s.logger.Info("webmail: codigo de verificacion rechazado", zap.String("username", username),
			zap.String("remote_ip", remoteIP), zap.Int("attempt", attempts))
		return "", domain.Session{}, domain.ErrInvalidMFACode
	case errors.Is(verr, domain.ErrMFANotEnabled):
		// La verificacion se restablecio entre los dos pasos: se vuelve a empezar con la contrasena.
		s.dropChallenge(ctx, key)
		return "", domain.Session{}, domain.ErrMFAChallengeExpired
	default:
		s.logger.Error("webmail: no se pudo validar el codigo de verificacion", zap.String("username", username), zap.Error(verr))
		return "", domain.Session{}, unavailable(verr)
	}
	// Sin borrar el desafio no se abre la sesion: seguiria admitiendo codigos.
	if err := s.mfaChallenges.DeleteChallenge(ctx, key); err != nil {
		return "", domain.Session{}, unavailable(err)
	}
	return s.openSession(ctx, ch.Identity, ch.StartedAt, remoteIP, previousToken)
}

// startChallenge guarda el desafio del segundo paso y devuelve su token, con la misma forma que el
// de una sesion (la celda de la instancia y 256 bits): en el almacen solo queda su hash.
func (s *Service) startChallenge(ctx context.Context, id domain.Identity, started time.Time) (string, error) {
	token, err := s.newToken()
	if err != nil {
		return "", unavailable(err)
	}
	key, _ := s.sessionKey(token)
	if err := s.mfaChallenges.CreateChallenge(ctx, key, domain.MFAChallenge{Identity: id, StartedAt: started}, s.cfg.MFAChallengeTTL); err != nil {
		s.logger.Error("webmail: no se pudo guardar el desafio del segundo paso", zap.String("username", id.Username), zap.Error(err))
		return "", unavailable(err)
	}
	return token, nil
}

func (s *Service) dropChallenge(ctx context.Context, key string) {
	if err := s.mfaChallenges.DeleteChallenge(ctx, key); err != nil {
		// El desafio caduca solo; hasta entonces el tope de intentos lo sigue cerrando.
		s.logger.Warn("webmail: no se pudo borrar el desafio del segundo paso", zap.Error(err))
	}
}

func challengeError(err error) error {
	if errors.Is(err, domain.ErrMFAChallengeExpired) {
		return err
	}
	return unavailable(err)
}

// openSession abre la sesion de una identidad ya verificada (por completo) y rota el token previo.
func (s *Service) openSession(ctx context.Context, id domain.Identity, started time.Time, remoteIP, previousToken string) (string, domain.Session, error) {
	s.discard(ctx, previousToken)

	token, err := s.newToken()
	if err != nil {
		return "", domain.Session{}, unavailable(err)
	}
	key, _ := s.sessionKey(token)
	sess := s.cfg.Sessions.Open(id, started)
	ttl, alive := s.cfg.Sessions.Remaining(sess, s.clock())
	if !alive {
		return "", domain.Session{}, unavailable(errors.New("la verificación superó la vida máxima de la sesión"))
	}
	if err := s.sessions.Create(ctx, key, sess, ttl); err != nil {
		s.logger.Error("webmail: no se pudo guardar la sesion", zap.String("username", id.Username), zap.Error(err))
		return "", domain.Session{}, unavailable(err)
	}
	s.logger.Info("webmail: inicio de sesion", zap.String("username", id.Username),
		zap.String("remote_ip", remoteIP), zap.String("session", key[:12]))
	return token, sess, nil
}

// Authenticate resuelve la sesion de un token y renueva su inactividad. Una sesion
// caducada o revocada se borra y se rechaza. Si el almacen no responde se falla cerrado:
// sin poder comprobar la revocacion no se sirve el buzon. El token de otra celda se rechaza
// sin buscarlo: aqui esa sesion no existe.
func (s *Service) Authenticate(ctx context.Context, token string) (domain.Session, error) {
	return s.authenticate(ctx, token, true)
}

// PeekSession comprueba la sesion igual que Authenticate pero sin renovar su inactividad: la usa el flujo de
// avisos, que se reconecta solo y no debe mantener viva una sesion que nadie usa.
func (s *Service) PeekSession(ctx context.Context, token string) (domain.Session, error) {
	return s.authenticate(ctx, token, false)
}

func (s *Service) authenticate(ctx context.Context, token string, touch bool) (domain.Session, error) {
	key, ok := s.sessionKey(token)
	if !ok {
		if cell, parsed := domain.ParseSessionToken(token); parsed && cell != s.cfg.CellCode {
			s.logger.Warn("webmail: token de otra celda rechazado sin buscarlo", zap.String("token_cell", cell))
		}
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
	if !touch {
		return sess, nil
	}
	if err := s.sessions.Touch(ctx, key, ttl); err != nil {
		// La sesion es valida: si no se pudo renovar, caducara antes, que es el lado seguro.
		s.logger.Warn("webmail: no se pudo renovar la inactividad de la sesion", zap.Error(err))
	}
	return sess, nil
}

// Logout destruye la sesion del token. Es idempotente.
func (s *Service) Logout(ctx context.Context, token string) error {
	key, ok := s.sessionKey(token)
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
	key, ok := s.sessionKey(token)
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

// sessionKey es la clave de almacen de un token de esta celda: su SHA-256. Un token de otra
// celda, o sin la forma de uno, no tiene clave y no se busca. Un volcado de Redis no entrega
// tokens utilizables.
func (s *Service) sessionKey(token string) (string, bool) {
	cell, ok := domain.ParseSessionToken(token)
	if !ok || cell != s.cfg.CellCode {
		return "", false
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:]), true
}

func (s *Service) newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return domain.NewSessionToken(s.cfg.CellCode, base64.RawURLEncoding.EncodeToString(b)), nil
}
