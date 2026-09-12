package ports

type EventPublisher interface {
	PublishUserCreated(tenantID, userID, email string) error
	// El user-agent viaja junto a la IP para que el detector de seguridad (servicio
	// audit) pueda reconocer inicios desde dispositivos nuevos.
	PublishUserLoggedIn(tenantID, userID, ip, userAgent string) error
	PublishUserLoggedOut(tenantID, userID string) error
	PublishUserLocked(tenantID, userID string) error
	// PublishLoginFailed alimenta la deteccion de fuerza bruta por IP.
	PublishLoginFailed(tenantID, userID, email, ip, userAgent string) error
	// PublishSessionRevoked deja rastro del cierre remoto de sesion por un admin.
	PublishSessionRevoked(tenantID, actorID, targetUserID, sessionID, ip string) error
	PublishPasswordChanged(tenantID, userID string) error
}
