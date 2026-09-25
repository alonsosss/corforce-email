package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
	"unicode"

	"github.com/alonsosss/corforce-email/pkg/auth"
	"github.com/alonsosss/corforce-email/pkg/totp"
	"github.com/alonsosss/corforce-email/services/identity/internal/domain"
	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// accessTokenBlockTTL es cuanto se retiene en la lista de bloqueo el access token de un
// logout: basta con cubrir su vida maxima, despues caduca solo.
const accessTokenBlockTTL = 15 * time.Minute

type AuthUseCase struct {
	users           ports.UserRepository
	sessions        ports.SessionRepository
	blocklist       ports.TokenBlocklistRepository
	policies        ports.PasswordPolicyRepository
	sessionPolicies ports.SessionPolicyRepository
	history         ports.PasswordHistoryRepository
	audit           ports.AuditRepository
	events          ports.EventPublisher
	tokens          *auth.TokenService
	tenants         ports.TenantRepository
	roles           ports.RoleLookup
	hasher          ports.PasswordHasher
	unknownLogins   ports.UnknownLoginRepository
	sealer          ports.SecretSealer
	// decoyHash es un hash del hasher, de una contrasena aleatoria que no se guarda: el
	// inicio de sesion que no tiene hash de cuenta que comparar compara contra el.
	decoyHash string
	logger    *zap.Logger
	now       func() time.Time
}

type AuthDeps struct {
	Users           ports.UserRepository
	Sessions        ports.SessionRepository
	Blocklist       ports.TokenBlocklistRepository
	Policies        ports.PasswordPolicyRepository
	SessionPolicies ports.SessionPolicyRepository
	History         ports.PasswordHistoryRepository
	Audit           ports.AuditRepository
	Events          ports.EventPublisher
	Tokens          *auth.TokenService
	Tenants         ports.TenantRepository
	Roles           ports.RoleLookup
	// Hasher es el mismo que escribe las contrasenas: de el sale el hash de relleno, con su
	// coste, al construir el caso de uso.
	Hasher ports.PasswordHasher
	// UnknownLogins cuenta los fallos de los correos sin cuenta: sin el, el bloqueo por
	// intentos confirmaria que la cuenta existe.
	UnknownLogins ports.UnknownLoginRepository
	// Sealer cifra el secreto TOTP (MAIL_ENCRYPTION_KEY). Obligatorio: sin el nadie podria
	// activar ni superar el segundo factor.
	Sealer ports.SecretSealer
	Logger *zap.Logger
	// Now es el reloj de las decisiones de sesion y bloqueo; nil es time.Now.
	Now func() time.Time
}

func NewAuthUseCase(deps AuthDeps) (*AuthUseCase, error) {
	if deps.Hasher == nil {
		return nil, errors.New("auth: falta el hasher de contrasenas")
	}
	if deps.UnknownLogins == nil {
		return nil, errors.New("auth: faltan los contadores de los correos sin cuenta")
	}
	if deps.Sealer == nil {
		return nil, errors.New("auth: falta el cifrado del secreto del segundo factor")
	}
	decoy, err := deps.Hasher.Hash(rand.Text())
	if err != nil {
		return nil, fmt.Errorf("auth: hash de relleno: %w", err)
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &AuthUseCase{
		hasher:          deps.Hasher,
		unknownLogins:   deps.UnknownLogins,
		sealer:          deps.Sealer,
		decoyHash:       decoy,
		now:             now,
		users:           deps.Users,
		sessions:        deps.Sessions,
		blocklist:       deps.Blocklist,
		policies:        deps.Policies,
		sessionPolicies: deps.SessionPolicies,
		history:         deps.History,
		audit:           deps.Audit,
		events:          deps.Events,
		tokens:          deps.Tokens,
		tenants:         deps.Tenants,
		roles:           deps.Roles,
		logger:          deps.Logger,
	}, nil
}

// loginCandidateLimit es el tope de cuentas que un correo puede resolver cuando el inicio de
// sesion no indica la empresa. Todo intento sin empresa gasta exactamente esa cantidad de
// comparaciones de contrasena (resolveByPassword), asi que subir el tope encarece cada inicio de
// sesion de la plataforma: se queda en lo que cubre el caso real, una persona que administra
// varias empresas con la misma direccion. Quien tenga su correo en mas empresas que el tope entra
// indicando la suya.
const loginCandidateLimit = 4

// Login autentica y abre sesion. Hay dos formas de identificarse y las dos responden lo mismo
// ante cualquier fallo (contrasena mala, correo sin cuenta, empresa que no resuelve):
//
//   - Con la empresa (tenant_slug): una sola cuenta posible y una sola comparacion de contrasena.
//   - Solo con el correo: hasta loginCandidateLimit cuentas, y entra la que coincide con la
//     contrasena. El intento gasta SIEMPRE loginCandidateLimit comparaciones, coincida la primera
//     o no exista ninguna cuenta, para que el tiempo no diga en cuantas empresas esta la
//     direccion, ni si existe, ni si resuelve empresa.
//
// La contrasena nunca se compara contra una cuenta que no puede tener sesion (inactive, pending o
// con el bloqueo vigente): esa comparacion va contra el hash de relleno. Un correo sin cuenta
// cuenta sus fallos como una cuenta y se bloquea al mismo umbral, con la misma respuesta: el
// bloqueo tampoco lo dice.
func (uc *AuthUseCase) Login(ctx context.Context, req ports.LoginRequest) (*ports.LoginResponse, error) {
	var user *domain.User
	var err error
	if req.TenantSlug != "" {
		user, err = uc.authenticateInTenant(ctx, req)
	} else {
		user, err = uc.authenticateByEmail(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	return uc.completeLogin(ctx, user, req)
}

// authenticateInTenant autentica en la empresa que indica el slug: una cuenta posible, la de ese
// correo en esa empresa.
func (uc *AuthUseCase) authenticateInTenant(ctx context.Context, req ports.LoginRequest) (*domain.User, error) {
	tenantID, err := uc.tenants.GetIDBySlug(ctx, req.TenantSlug)
	if err != nil {
		uc.compareDecoy(req.Password)
		return nil, uc.unknownFailure(ctx, scopeSlug+req.TenantSlug, uuid.Nil, req.Email, domain.ErrTenantNotFound)
	}

	user, err := uc.users.GetByEmail(ctx, tenantID, req.Email)
	if err != nil {
		uc.compareDecoy(req.Password)
		return nil, uc.unknownFailure(ctx, scopeTenant+tenantID.String(), tenantID, req.Email, domain.ErrInvalidCredentials)
	}

	// Una cuenta que no puede tener sesion (inactive, pending o con el bloqueo vigente) se
	// rechaza sin mirar su contrasena, como siempre se hizo con inactive y locked: no cuenta
	// un intento fallido ni ofrece un oraculo de la contrasena. Compara contra el relleno
	// para no tardar menos que el resto de fallos. Un bloqueo caducado deja pasar y se
	// retira al entrar (ResetFailedAttempts).
	if err := user.SessionAllowed(uc.now()); err != nil {
		uc.compareDecoy(req.Password)
		return nil, err
	}

	if err := uc.hasher.Compare(user.PasswordHash, req.Password); err != nil {
		uc.handleFailedLogin(ctx, user, tenantID, req.IPAddress, req.UserAgent)
		return nil, domain.ErrInvalidCredentials
	}
	return user, nil
}

// authenticateByEmail resuelve la empresa por la credencial: de las cuentas que el correo tiene
// en la plataforma entra la que coincide con la contrasena. Antes se resolvia la empresa por el
// correo y se comparaba contra una sola cuenta, asi que una direccion dada de alta en dos
// empresas entraba siempre en la misma y la otra recibia "credenciales invalidas".
func (uc *AuthUseCase) authenticateByEmail(ctx context.Context, req ports.LoginRequest) (*domain.User, error) {
	candidates, err := uc.users.ListLoginCandidates(ctx, req.Email, loginCandidateLimit)
	if err != nil {
		// Sin poder leer las cuentas no se autentica a nadie, y se responde como a un correo
		// sin cuenta: gastando las mismas comparaciones, para no delatarse por el tiempo.
		uc.logger.Warn("no se pudieron leer las cuentas del correo", zap.Error(err))
		candidates = nil
	}

	now := uc.now()
	resolved := uc.resolveByPassword(req.Password, candidates, now)
	if resolved.match != nil {
		if resolved.matches > 1 {
			// La misma direccion y la misma contrasena en dos empresas: entra la cuenta mas
			// antigua, siempre la misma, y quien quiera la otra indica su empresa.
			uc.logger.Warn("el correo resuelve varias cuentas con la misma contrasena; entra la mas antigua",
				zap.String("user_id", resolved.match.ID.String()), zap.Int("coincidencias", resolved.matches))
		}
		return resolved.match, nil
	}
	if len(candidates) == 0 {
		return nil, uc.unknownFailure(ctx, scopeNoTenant, uuid.Nil, req.Email, domain.ErrTenantNotFound)
	}
	if len(resolved.eligible) == 0 {
		// Ninguna cuenta del correo puede tener sesion: la misma respuesta que da una sola. Las
		// candidatas solo llegan active o locked, asi que aqui solo cabe el bloqueo vigente; si
		// una lectura devolviera otra cosa, el fallo sigue siendo "credenciales invalidas" y
		// nadie entra sin comparar su contrasena.
		if err := candidates[0].SessionAllowed(now); err != nil {
			return nil, err
		}
		return nil, domain.ErrInvalidCredentials
	}
	// El fallo se cuenta en cada cuenta que podia entrar: omitir la empresa no puede ser la
	// forma de probar contrasenas sin gastar los intentos de ninguna cuenta.
	for _, u := range resolved.eligible {
		uc.handleFailedLogin(ctx, u, u.TenantID, req.IPAddress, req.UserAgent)
	}
	return nil, domain.ErrInvalidCredentials
}

// candidateMatch es el resultado de comparar la contrasena contra las cuentas de un correo.
type candidateMatch struct {
	// match es la cuenta que entra: la primera, en el orden estable del repositorio, cuya
	// contrasena coincide.
	match *domain.User
	// matches son cuantas coincidieron. Mas de una es una ambiguedad real: la misma direccion
	// con la misma contrasena en varias empresas.
	matches int
	// eligible son las cuentas que podian abrir sesion, coincidiera su contrasena o no; son las
	// unicas a las que se les cuenta un intento fallido.
	eligible []*domain.User
}

// resolveByPassword compara la contrasena contra las cuentas que pueden abrir sesion y gasta
// SIEMPRE loginCandidateLimit comparaciones: las que sobran van contra el hash de relleno. Ni
// cuantas cuentas tiene el correo ni en que estado estan cambian lo que tarda el intento.
func (uc *AuthUseCase) resolveByPassword(password string, candidates []*domain.User, now time.Time) candidateMatch {
	var out candidateMatch
	spent := 0
	for _, u := range candidates {
		// El tope es del repositorio; la guarda evita que una lectura con mas filas de las
		// pedidas multiplique el coste del intento.
		if spent >= loginCandidateLimit || u.SessionAllowed(now) != nil {
			continue
		}
		out.eligible = append(out.eligible, u)
		spent++
		if uc.hasher.Compare(u.PasswordHash, password) != nil {
			continue
		}
		out.matches++
		if out.match == nil {
			out.match = u
		}
	}
	for ; spent < loginCandidateLimit; spent++ {
		uc.compareDecoy(password)
	}
	return out
}

// completeLogin es el tramo comun a las dos formas de identificarse, ya con la cuenta
// autenticada: apunta el inicio y abre sesion.
func (uc *AuthUseCase) completeLogin(ctx context.Context, user *domain.User, req ports.LoginRequest) (*ports.LoginResponse, error) {
	uc.recordLogin(ctx, user, req.Password)

	// Con MFA activo no se entrega el par todavia: sale un token de desafio de corta
	// vida y la sesion se abre al validar el codigo (VerifyMFAChallenge).
	if user.MFAEnabled {
		challengeToken, err := uc.tokens.GenerateMFAChallenge(user.ID.String(), user.TenantID.String())
		if err != nil {
			return nil, fmt.Errorf("generate mfa challenge: %w", err)
		}
		uc.audit.Log(ctx, &domain.AuditEntry{
			ID:        uuid.New(),
			TenantID:  user.TenantID,
			UserID:    user.ID,
			Action:    "mfa_challenge_issued",
			Resource:  "session",
			IPAddress: req.IPAddress,
			UserAgent: req.UserAgent,
			CreatedAt: uc.now(),
		})
		return &ports.LoginResponse{
			MFARequired: true,
			MFAToken:    challengeToken,
		}, nil
	}

	return uc.openSession(ctx, user, user.TenantID, req.IPAddress, req.UserAgent, "login")
}

// openSession emite el par de tokens y persiste la sesion nueva; es el tramo comun del
// login directo y del login con MFA. Corre el limite de simultaneas DESPUES de crear la
// sesion para que cuente la que acaba de abrirse: quien entra ahora se queda.
func (uc *AuthUseCase) openSession(ctx context.Context, user *domain.User, tenantID uuid.UUID, ip, userAgent, action string) (*ports.LoginResponse, error) {
	roles := uc.roleNames(ctx, user.ID)
	pair, err := uc.tokens.GeneratePair(user.ID.String(), tenantID.String(), roles)
	if err != nil {
		return nil, fmt.Errorf("generate tokens: %w", err)
	}

	policy := uc.sessionPolicy(ctx, tenantID)
	now := uc.now()
	session := &domain.Session{
		ID:               uuid.New(),
		UserID:           user.ID,
		RefreshTokenHash: hashToken(pair.RefreshToken),
		IPAddress:        ip,
		UserAgent:        userAgent,
		ExpiresAt:        now.Add(policy.RefreshTTL()),
		CreatedAt:        now,
		LoginAt:          now,
	}
	if err := uc.sessions.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	uc.enforceConcurrentLimit(ctx, user.ID, policy)

	uc.audit.Log(ctx, &domain.AuditEntry{
		ID:        uuid.New(),
		TenantID:  tenantID,
		UserID:    user.ID,
		Action:    action,
		Resource:  "session",
		IPAddress: ip,
		UserAgent: userAgent,
		CreatedAt: now,
	})

	// El detector de seguridad (IP o dispositivo nuevos) escucha este evento; se publica
	// en ambos caminos de entrada, con o sin segundo factor.
	uc.events.PublishUserLoggedIn(tenantID.String(), user.ID.String(), ip, userAgent)

	return &ports.LoginResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		TokenType:    pair.TokenType,
		UserID:       user.ID.String(),
		TenantID:     tenantID.String(),
		Roles:        roles,
	}, nil
}

// roleNames resuelve los roles que viajan en el token. Si la lectura falla se emite el
// token SIN roles y queda constancia: el usuario entra pero no puede hacer nada
// reservado a un rol, que es el fallo seguro. Nunca se inventa un rol por defecto.
func (uc *AuthUseCase) roleNames(ctx context.Context, userID uuid.UUID) []string {
	roles, err := uc.roles.RoleNames(ctx, userID)
	if err != nil {
		uc.logger.Warn("no se pudieron leer los roles del usuario; el token sale sin roles",
			zap.String("user_id", userID.String()), zap.Error(err))
		return []string{}
	}
	return roles
}

// RoleNamesOf devuelve los roles vigentes de un usuario. A diferencia de roleNames, el
// fallo se propaga: quien pregunta decide una autorizacion y no puede suponer "sin roles".
func (uc *AuthUseCase) RoleNamesOf(ctx context.Context, userID uuid.UUID) ([]string, error) {
	return uc.roles.RoleNames(ctx, userID)
}

// sessionPolicy lee la politica de la empresa. Nunca falla la autenticacion por esto:
// si la consulta no responde se opera con los valores por defecto, que son el
// comportamiento historico. Endurecer la sesion no puede convertirse en no poder entrar.
func (uc *AuthUseCase) sessionPolicy(ctx context.Context, tenantID uuid.UUID) *domain.SessionPolicy {
	if uc.sessionPolicies == nil {
		return domain.DefaultSessionPolicy(tenantID)
	}
	p, err := uc.sessionPolicies.Get(ctx, tenantID)
	if err != nil || p == nil {
		if err != nil {
			uc.logger.Warn("politica de sesion no disponible; se usan los valores por defecto",
				zap.String("tenant_id", tenantID.String()), zap.Error(err))
		}
		return domain.DefaultSessionPolicy(tenantID)
	}
	return p
}

// enforceConcurrentLimit cierra las sesiones mas antiguas cuando el usuario supera el
// tope de la empresa.
func (uc *AuthUseCase) enforceConcurrentLimit(ctx context.Context, userID uuid.UUID, policy *domain.SessionPolicy) {
	if policy == nil || policy.MaxConcurrentSessions <= 0 {
		return
	}
	revoked, err := uc.sessions.RevokeOldestByUser(ctx, userID, policy.MaxConcurrentSessions)
	if err != nil {
		uc.logger.Warn("no se pudo aplicar el limite de sesiones simultaneas",
			zap.String("user_id", userID.String()), zap.Error(err))
		return
	}
	if revoked > 0 {
		uc.logger.Info("sesiones antiguas cerradas por el limite de la empresa",
			zap.String("user_id", userID.String()), zap.Int("cerradas", revoked),
			zap.Int("limite", policy.MaxConcurrentSessions))
	}
}

func (uc *AuthUseCase) RefreshToken(ctx context.Context, refreshToken string) (*ports.LoginResponse, error) {
	hash := hashToken(refreshToken)

	session, err := uc.sessions.GetByRefreshTokenHash(ctx, hash)
	if err != nil {
		return nil, domain.ErrSessionNotFound
	}

	// Ventana de gracia para la rotacion: con micro-frontends, varias copias del
	// cliente pueden refrescar a la vez con el mismo token. El primero rota (revoca
	// el anterior); los demas llegan con un token recien revocado. Eso es una
	// CARRERA legitima, no robo: dentro de la gracia se emiten tokens validos igual.
	// Reuso de un token revocado hace tiempo si es robo: se revocan TODAS.
	const refreshReuseGrace = 60 * time.Second
	if session.Revoked {
		// La gracia solo cubre la carrera de la rotacion normal. Un cierre deliberado
		// ("revoked": logout, logout-all o revocacion de admin) no revive por renovar rapido.
		if session.RevokedReason != "rotated" {
			return nil, domain.ErrSessionRevoked
		}
		if session.RevokedAt == nil || uc.now().Sub(*session.RevokedAt) > refreshReuseGrace {
			uc.sessions.RevokeAllByUser(ctx, session.UserID)
			return nil, domain.ErrSessionRevoked
		}
		// Dentro de la gracia: continua al flujo de emision (no se revoca de nuevo).
	} else if uc.now().After(session.ExpiresAt) {
		uc.sessions.Revoke(ctx, session.ID)
		return nil, domain.ErrSessionExpired
	}

	user, err := uc.users.GetByID(ctx, session.UserID)
	if err != nil {
		return nil, domain.ErrUserNotFound
	}

	// La misma regla que el inicio de sesion: un bloqueo caducado no impide renovar.
	if err := user.SessionAllowed(uc.now()); err != nil {
		return nil, err
	}

	// Cierre por inactividad: la ultima renovacion (created_at de la fila vigente) es
	// la ultima senal de vida del dispositivo. Se evalua aqui y no con un barrido
	// periodico para que la politica surta efecto sin depender de un proceso externo.
	policy := uc.sessionPolicy(ctx, user.TenantID)
	if policy.IdleExceeded(session.CreatedAt, uc.now()) {
		uc.sessions.Revoke(ctx, session.ID)
		return nil, domain.ErrSessionIdle
	}

	// Solo rota si no estaba ya revocada (en la carrera dentro de la gracia ya lo esta).
	if !session.Revoked {
		uc.sessions.RevokeForRotation(ctx, session.ID)
	}

	// Los roles se releen en cada renovacion: retirar un rol surte efecto en la
	// siguiente renovacion sin esperar a que caduque el refresh token.
	roles := uc.roleNames(ctx, user.ID)
	pair, err := uc.tokens.GeneratePair(user.ID.String(), user.TenantID.String(), roles)
	if err != nil {
		return nil, fmt.Errorf("generate tokens: %w", err)
	}

	newSession := &domain.Session{
		ID:               uuid.New(),
		UserID:           user.ID,
		RefreshTokenHash: hashToken(pair.RefreshToken),
		IPAddress:        session.IPAddress,
		UserAgent:        session.UserAgent,
		// La renovacion no extiende la sesion mas alla de su ventana original: si no,
		// una politica de 8 horas se volveria eterna renovando cada 15 minutos.
		ExpiresAt: session.ExpiresAt,
		CreatedAt: uc.now(),
		// La rotacion conserva el inicio de sesion original de la cadena.
		LoginAt: session.LoginAt,
	}
	uc.sessions.Create(ctx, newSession)

	return &ports.LoginResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		TokenType:    pair.TokenType,
		UserID:       user.ID.String(),
		TenantID:     user.TenantID.String(),
		Roles:        roles,
	}, nil
}

// Logout cierra la sesion actual: bloquea el access token hasta que caduque y, si el
// cliente entrego su refresh token (cookie o JSON), revoca esa sesion en el servidor
// para que el refresh deje de servir aunque alguien lo hubiera copiado. Solo se revoca
// si la sesion es del propio usuario: un refresh ajeno no cierra la sesion de otro.
func (uc *AuthUseCase) Logout(ctx context.Context, tenantID, userID uuid.UUID, accessToken, refreshToken string) error {
	if accessToken != "" {
		uc.blocklist.Add(ctx, hashToken(accessToken), uc.now().Add(accessTokenBlockTTL))
	}
	if refreshToken != "" {
		if session, err := uc.sessions.GetByRefreshTokenHash(ctx, hashToken(refreshToken)); err == nil && session.UserID == userID && !session.Revoked {
			if err := uc.sessions.Revoke(ctx, session.ID); err != nil {
				uc.logger.Warn("logout: no se pudo revocar la sesion del refresh token",
					zap.String("user_id", userID.String()), zap.Error(err))
			}
		}
	}
	uc.events.PublishUserLoggedOut(tenantID.String(), userID.String())
	return nil
}

func (uc *AuthUseCase) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	if err := uc.sessions.RevokeAllByUser(ctx, userID); err != nil {
		return err
	}
	// Adelanta el epoch de revocacion: invalida al instante los access token ya
	// emitidos (el gateway los rechaza), no solo los refresh. Sin esto, revocar
	// dejaba vivo el access token hasta que caducara. Best-effort: si falla, la
	// revocacion del refresh sigue en pie y el access caduca solo.
	_ = uc.users.BumpTokenEpoch(ctx, userID)
	return nil
}

func (uc *AuthUseCase) handleFailedLogin(ctx context.Context, user *domain.User, tenantID uuid.UUID, ip, userAgent string) {
	now := uc.now()
	attempts, countErr := uc.users.IncrementFailedAttempts(ctx, user.ID, now)

	// Rastro para el detector de fuerza bruta (servicio audit): cada intento
	// fallido con su IP y dispositivo.
	uc.events.PublishLoginFailed(tenantID.String(), user.ID.String(), user.Email, ip, userAgent)

	if countErr != nil {
		uc.logger.Warn("no se pudo contar el inicio fallido",
			zap.String("user_id", user.ID.String()), zap.Error(countErr))
		return
	}
	policy, err := uc.policies.Get(ctx, tenantID)
	if err != nil {
		return
	}

	if attempts >= policy.MaxFailedAttempts {
		lockUntil := now.Add(time.Duration(policy.LockoutDurationMinutes) * time.Minute)
		uc.users.LockUser(ctx, user.ID, &lockUntil)
		uc.events.PublishUserLocked(tenantID.String(), user.ID.String())
		uc.audit.Log(ctx, &domain.AuditEntry{
			ID:         uuid.New(),
			TenantID:   tenantID,
			UserID:     user.ID,
			Action:     "account_locked",
			Resource:   "user",
			ResourceID: user.ID.String(),
			IPAddress:  ip,
			CreatedAt:  now,
		})
	}
}

// Ambitos del contador de un correo sin cuenta: la empresa en que se busco, el slug que no
// resolvio ninguna o, sin slug, ninguna empresa. Una cuenta comparte su contador entre los
// caminos que llegan a ella; un correo sin cuenta se comporta como una cuenta de una empresa
// que no se nombra.
const (
	scopeTenant   = "tenant:"
	scopeSlug     = "slug:"
	scopeNoTenant = "none"
)

// unknownFailure cuenta el fallo de un correo sin cuenta con la politica que tendria la cuenta:
// la de la empresa si se resolvio y, sin empresa, la que el repositorio da a uuid.Nil (la de por
// defecto). Pasado el umbral responde ErrAccountLocked, como una cuenta bloqueada; si no, failure.
func (uc *AuthUseCase) unknownFailure(ctx context.Context, scope string, tenantID uuid.UUID, email string, failure error) error {
	policy, err := uc.policies.Get(ctx, tenantID)
	if err != nil {
		return failure
	}
	lockout := time.Duration(policy.LockoutDurationMinutes) * time.Minute
	wasLocked, err := uc.unknownLogins.RecordFailure(ctx, unknownSubject(scope, email), uc.now(), policy.MaxFailedAttempts, lockout)
	if err != nil {
		uc.logger.Warn("no se pudo contar el inicio fallido de un correo sin cuenta", zap.Error(err))
		return failure
	}
	if wasLocked {
		return domain.ErrAccountLocked
	}
	return failure
}

// unknownSubject es la clave del contador de un correo sin cuenta: el ambito y el correo tal
// como se buscaron, sin normalizar, porque la busqueda tampoco normaliza (dos grafias de una
// cuenta real llegan a contadores distintos, y las de un correo sin cuenta tambien). Solo se
// guarda su SHA-256; la longitud del ambito separa las dos partes sin ambiguedad.
func unknownSubject(scope, email string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s%s", len(scope), scope, email)))
	return hex.EncodeToString(sum[:])
}

// recordLogin apunta el inicio correcto. Si el hash de la cuenta tiene otro coste que el del
// hasher lo rehace con la contrasena que acaba de coincidir y lo guarda en la misma sentencia:
// una cuenta con otro coste se distinguia por el tiempo de un fallo. La contrasena no se
// registra en ningun caso.
func (uc *AuthUseCase) recordLogin(ctx context.Context, user *domain.User, password string) {
	var rehash *ports.PasswordRehash
	if uc.hasher.NeedsRehash(user.PasswordHash) {
		hash, err := uc.hasher.Hash(password)
		if err != nil {
			uc.logger.Warn("no se pudo rehacer el hash con el coste vigente",
				zap.String("user_id", user.ID.String()), zap.Error(err))
		} else {
			rehash = &ports.PasswordRehash{Current: user.PasswordHash, Replacement: hash}
		}
	}
	if err := uc.users.RecordLogin(ctx, user.ID, rehash); err != nil {
		uc.logger.Warn("no se pudo apuntar el inicio de sesion",
			zap.String("user_id", user.ID.String()), zap.Error(err))
	}
}

// compareDecoy gasta lo mismo que comparar la contrasena de una cuenta y descarta el
// resultado.
func (uc *AuthUseCase) compareDecoy(password string) {
	_ = uc.hasher.Compare(uc.decoyHash, password)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// SetupMFA genera un secreto TOTP nuevo y devuelve la URI de aprovisionamiento. El
// secreto NO se persiste hasta que ActivateMFA reciba un codigo valido.
func (uc *AuthUseCase) SetupMFA(ctx context.Context, userID uuid.UUID, email, issuer string) (secret, uri string, err error) {
	secret, err = totp.GenerateSecret()
	if err != nil {
		return "", "", fmt.Errorf("generate totp secret: %w", err)
	}
	uri = totp.ProvisioningURI(secret, email, issuer)
	return secret, uri, nil
}

// ActivateMFA valida el codigo TOTP contra el secreto dado y, si es correcto, guarda el
// secreto cifrado y activa el segundo factor del usuario. El paso del codigo de activacion
// queda como el ultimo usado: no vale despues como segundo factor.
func (uc *AuthUseCase) ActivateMFA(ctx context.Context, userID uuid.UUID, secret, code string) error {
	step, ok := totp.ValidateStep(secret, code, uc.now())
	if !ok {
		return domain.ErrInvalidMFACode
	}
	sealed, err := uc.sealer.EncryptWithAAD([]byte(secret), domain.MFASecretAAD(userID))
	if err != nil {
		return fmt.Errorf("cifrar el secreto del segundo factor: %w", err)
	}
	if err := uc.users.EnableMFA(ctx, userID, sealed, step); err != nil {
		return fmt.Errorf("enable mfa: %w", err)
	}
	return nil
}

// DisableMFA borra el secreto y desactiva el segundo factor, pero solo tras re-probar
// identidad: contrasena actual Y un codigo TOTP valido.
//
// Sin esto, una sesion robada apagaba el segundo factor de la victima sin conocer
// ni su contrasena ni su codigo, destruyendo la unica defensa que le quedaba. Es un
// paso de "modo sudo": tener sesion no basta para desarmar el 2FA.
func (uc *AuthUseCase) DisableMFA(ctx context.Context, userID uuid.UUID, currentPassword, code string) error {
	user, err := uc.users.GetByID(ctx, userID)
	if err != nil {
		return domain.ErrUserNotFound
	}
	if !user.MFAEnabled {
		return nil
	}
	if uc.hasher.Compare(user.PasswordHash, currentPassword) != nil {
		return domain.ErrInvalidCredentials
	}
	if err := uc.consumeTOTP(ctx, user, code); err != nil {
		return err
	}
	return uc.users.DisableMFA(ctx, userID)
}

// mfaSecret abre el secreto TOTP de la cuenta. El secreto en claro de una fila anterior al
// cifrado vale mientras no se cifre: si hay uno, lo escribio el codigo anterior y es el vigente.
func (uc *AuthUseCase) mfaSecret(user *domain.User) (string, error) {
	if user.MFASecretLegacy != "" {
		return user.MFASecretLegacy, nil
	}
	if len(user.MFASecretSealed) == 0 {
		return "", errors.New("segundo factor activo sin secreto guardado")
	}
	plain, err := uc.sealer.DecryptWithAAD(user.MFASecretSealed, domain.MFASecretAAD(user.ID))
	if err != nil {
		return "", fmt.Errorf("abrir el secreto del segundo factor: %w", err)
	}
	if len(plain) == 0 {
		return "", errors.New("segundo factor activo con un secreto vacio")
	}
	return string(plain), nil
}

// consumeTOTP acepta el codigo solo si es valido y su paso es posterior al ultimo aceptado, y
// lo apunta en la misma sentencia: el mismo codigo no vale dos veces, ni aunque lleguen a la
// vez. Un secreto que no se puede abrir es un fallo del servicio (una llave retirada antes de
// tiempo), no un codigo incorrecto: se registra y se devuelve como error interno.
func (uc *AuthUseCase) consumeTOTP(ctx context.Context, user *domain.User, code string) error {
	secret, err := uc.mfaSecret(user)
	if err != nil {
		uc.logger.Error("no se pudo leer el secreto del segundo factor",
			zap.String("user_id", user.ID.String()), zap.Error(err))
		return err
	}
	step, ok := totp.ValidateStep(secret, code, uc.now())
	if !ok {
		return domain.ErrInvalidMFACode
	}
	accepted, err := uc.users.AdvanceMFAStep(ctx, user.ID, step)
	if err != nil {
		return fmt.Errorf("apuntar el paso del segundo factor: %w", err)
	}
	if !accepted {
		return domain.ErrInvalidMFACode
	}
	return nil
}

// MFASecretSweep resume una pasada de SealLegacyMFASecrets.
type MFASecretSweep struct {
	// Dropped son las cuentas sin segundo factor activo a las que se les borro el secreto;
	// Sealed, los secretos en claro que quedaron cifrados; Changed, las filas que otro escribio
	// mientras se cifraban (otra replica, o una activacion): si siguen en claro, la proxima
	// pasada las cifra.
	Dropped, Sealed, Changed int
}

// SealLegacyMFASecrets cifra los secretos TOTP que quedan en claro de antes del cifrado y borra
// los de las cuentas sin segundo factor activo. Es idempotente y no toma cerrojos: cada fila se
// sustituye solo si sigue guardando lo leido, asi que varias replicas pueden correrlo a la vez.
func (uc *AuthUseCase) SealLegacyMFASecrets(ctx context.Context, batch int) (MFASecretSweep, error) {
	var out MFASecretSweep
	if batch <= 0 {
		return out, errors.New("el lote del barrido debe ser positivo")
	}
	dropped, err := uc.users.DropDisabledMFASecrets(ctx)
	if err != nil {
		return out, fmt.Errorf("borrar los secretos de cuentas sin segundo factor: %w", err)
	}
	out.Dropped = int(dropped)
	after := uuid.Nil
	for {
		rows, err := uc.users.ListPlainMFASecrets(ctx, after, batch)
		if err != nil {
			return out, fmt.Errorf("leer los secretos en claro: %w", err)
		}
		for _, r := range rows {
			after = r.UserID
			sealed, err := uc.sealer.EncryptWithAAD([]byte(r.Secret), domain.MFASecretAAD(r.UserID))
			if err != nil {
				return out, fmt.Errorf("cifrar el secreto del segundo factor: %w", err)
			}
			ok, err := uc.users.SealPlainMFASecret(ctx, r.UserID, r.Secret, sealed)
			if err != nil {
				return out, fmt.Errorf("guardar el secreto cifrado: %w", err)
			}
			if ok {
				out.Sealed++
			} else {
				out.Changed++
			}
		}
		if len(rows) < batch {
			return out, nil
		}
	}
}

// StepUp re-verifica la identidad (contrasena y, si la tiene, MFA) y emite un token
// de step-up de corta vida para desbloquear acciones criticas. No crea sesion.
func (uc *AuthUseCase) StepUp(ctx context.Context, userID uuid.UUID, currentPassword, code string) (string, error) {
	user, err := uc.users.GetByID(ctx, userID)
	if err != nil {
		return "", domain.ErrUserNotFound
	}
	if uc.hasher.Compare(user.PasswordHash, currentPassword) != nil {
		return "", domain.ErrInvalidCredentials
	}
	if user.MFAEnabled {
		if err := uc.consumeTOTP(ctx, user, code); err != nil {
			return "", err
		}
	}
	return uc.tokens.GenerateStepUp(user.ID.String(), user.TenantID.String())
}

// VerifyMFAChallenge valida el token de desafio y el codigo TOTP; con exito abre la
// sesion y entrega el par completo.
func (uc *AuthUseCase) VerifyMFAChallenge(ctx context.Context, challengeToken, code, ip, ua string) (*ports.LoginResponse, error) {
	userIDStr, tenantIDStr, err := uc.tokens.ValidateMFAChallenge(challengeToken)
	if err != nil {
		return nil, domain.ErrInvalidMFACode
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return nil, domain.ErrInvalidMFACode
	}

	user, err := uc.users.GetByID(ctx, userID)
	if err != nil {
		return nil, domain.ErrUserNotFound
	}

	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		return nil, domain.ErrInvalidMFACode
	}

	// El segundo factor se somete al mismo bloqueo por cuenta que la contrasena:
	// sin esto, con la contrasena ya obtenida, el espacio de 6 digitos del TOTP es
	// forzable (el limite por IP se reparte entre varias). El token de desafio
	// dura 5 minutos, asi que la ventana de intentos es corta y ademas se cierra
	// al alcanzar el umbral de intentos fallidos de la politica del tenant. La cuenta
	// desactivada durante los 5 minutos del reto tampoco termina de entrar.
	if err := user.SessionAllowed(uc.now()); err != nil {
		return nil, err
	}

	if err := uc.consumeTOTP(ctx, user, code); err != nil {
		if errors.Is(err, domain.ErrInvalidMFACode) {
			uc.handleFailedLogin(ctx, user, tenantID, ip, ua)
		}
		return nil, err
	}

	uc.users.ResetFailedAttempts(ctx, user.ID)

	return uc.openSession(ctx, user, tenantID, ip, ua, "login_mfa")
}

func ValidatePasswordPolicy(password string, policy *domain.PasswordPolicy) error {
	if len(password) < policy.MinLength {
		return domain.ErrPasswordPolicyFail
	}

	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, c := range password {
		switch {
		case unicode.IsUpper(c):
			hasUpper = true
		case unicode.IsLower(c):
			hasLower = true
		case unicode.IsDigit(c):
			hasDigit = true
		case unicode.IsPunct(c) || unicode.IsSymbol(c):
			hasSpecial = true
		}
	}

	if policy.RequireUppercase && !hasUpper {
		return domain.ErrPasswordPolicyFail
	}
	if policy.RequireLowercase && !hasLower {
		return domain.ErrPasswordPolicyFail
	}
	if policy.RequireDigit && !hasDigit {
		return domain.ErrPasswordPolicyFail
	}
	if policy.RequireSpecial && !hasSpecial {
		return domain.ErrPasswordPolicyFail
	}

	return nil
}

// GetSessionPolicy expone la politica vigente de la empresa (la configurada o la por
// defecto) para la pantalla de configuracion.
func (uc *AuthUseCase) GetSessionPolicy(ctx context.Context, tenantID uuid.UUID) (*domain.SessionPolicy, error) {
	if uc.sessionPolicies == nil {
		return domain.DefaultSessionPolicy(tenantID), nil
	}
	return uc.sessionPolicies.Get(ctx, tenantID)
}

// SaveSessionPolicy valida y guarda la politica. No toca las sesiones vivas: acortar
// la ventana no puede echar a todo el mundo de golpe; el nuevo valor rige desde el
// proximo inicio de sesion, y el limite de simultaneas desde el proximo login.
func (uc *AuthUseCase) SaveSessionPolicy(ctx context.Context, p *domain.SessionPolicy, actorID uuid.UUID) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if uc.sessionPolicies == nil {
		return domain.ErrInvalidSessionPolicy
	}
	p.UpdatedBy = &actorID
	if err := uc.sessionPolicies.Upsert(ctx, p); err != nil {
		return err
	}
	uc.audit.Log(ctx, &domain.AuditEntry{
		ID:       uuid.New(),
		TenantID: p.TenantID,
		UserID:   actorID,
		Action:   "session_policy_updated",
		Resource: "session_policy",
		Details: map[string]interface{}{
			"refresh_ttl_hours":       p.RefreshTTLHours,
			"max_concurrent_sessions": p.MaxConcurrentSessions,
			"idle_timeout_minutes":    p.IdleTimeoutMinutes,
		},
		CreatedAt: uc.now(),
	})
	return nil
}

// ListSessions lista sesiones para la vista de dispositivos. El alcance ya viene
// resuelto en el filtro por el handler (tenant propio, o todas las empresas cuando
// lo pide el superadmin de plataforma).
func (uc *AuthUseCase) ListSessions(ctx context.Context, filter domain.SessionFilter) ([]*domain.SessionInfo, int64, error) {
	return uc.sessions.ListInfo(ctx, filter)
}

// RevokeSessionScoped revoca una sesion validando el alcance del actor: el admin de
// una empresa solo puede cerrar sesiones de usuarios de SU tenant; el superadmin de
// plataforma puede cerrar cualquiera. Deja rastro en la bitacora de identity y
// publica el evento para el registro de seguridad del tenant.
func (uc *AuthUseCase) RevokeSessionScoped(ctx context.Context, sessionID, actorID uuid.UUID, actorTenantID *uuid.UUID, actorIP string) error {
	info, err := uc.sessions.GetInfo(ctx, sessionID)
	if err != nil {
		return err
	}
	if actorTenantID != nil && info.TenantID != *actorTenantID {
		return domain.ErrSessionNotFound
	}
	if err := uc.sessions.Revoke(ctx, sessionID); err != nil {
		return err
	}
	uc.audit.Log(ctx, &domain.AuditEntry{
		ID:         uuid.New(),
		TenantID:   info.TenantID,
		UserID:     actorID,
		Action:     "session_revoked",
		Resource:   "session",
		ResourceID: sessionID.String(),
		Details: map[string]interface{}{
			"target_user_id": info.UserID.String(),
			"target_email":   info.UserEmail,
			"ip_address":     info.IPAddress,
		},
		CreatedAt: uc.now(),
	})
	uc.events.PublishSessionRevoked(info.TenantID.String(), actorID.String(), info.UserID.String(), sessionID.String(), actorIP)
	return nil
}

// Los casos de uso cumplen los puertos de servicio; si una firma cambia, falla aqui y
// no en el handler que la consume.
var _ ports.AuthService = (*AuthUseCase)(nil)
