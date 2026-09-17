package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/alonsosss/corforce-email/services/audit/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SecurityDetectorConfig acota las heuristicas. Los umbrales llegan por entorno
// (SECURITY_BRUTEFORCE_MAX / SECURITY_BRUTEFORCE_WINDOW_MIN) para poder ajustarlos
// sin recompilar, ya validados en su rango al arrancar (services/audit/main.go).
type SecurityDetectorConfig struct {
	// BruteForceMax es el numero de logins fallidos desde una misma IP dentro de la
	// ventana a partir del cual se considera fuerza bruta.
	BruteForceMax int64
	// BruteForceWindow es la ventana de observacion de los fallos.
	BruteForceWindow time.Duration
}

// SecurityDetector convierte el flujo de eventos de identidad en eventos de
// seguridad accionables: inicio desde IP nueva, dispositivo nuevo, rafaga de
// fallos (fuerza bruta), cuenta bloqueada y cierre remoto de sesion. Trabaja
// sobre la propia bitacora (audit_logs) como historial: el detector corre DESPUES
// de que el consumidor persistio el evento.
type SecurityDetector struct {
	logs     ports.AuditLogRepository
	security ports.SecurityEventRepository
	events   ports.EventPublisher
	cfg      SecurityDetectorConfig
	logger   *zap.Logger
}

func NewSecurityDetector(logs ports.AuditLogRepository, security ports.SecurityEventRepository,
	events ports.EventPublisher, cfg SecurityDetectorConfig, logger *zap.Logger) *SecurityDetector {
	return &SecurityDetector{logs: logs, security: security, events: events, cfg: cfg, logger: logger}
}

// Inspect evalua un registro recien persistido. Nunca falla la peticion original:
// los errores solo se registran.
func (d *SecurityDetector) Inspect(ctx context.Context, l *domain.AuditLog, userAgent string) {
	switch l.Action {
	case "user.logged_in":
		d.inspectLogin(ctx, l, userAgent)
	case "user.login_failed":
		d.inspectFailedLogin(ctx, l)
	case "user.locked":
		d.raise(ctx, l, "account_locked", "high",
			"Cuenta bloqueada por intentos fallidos repetidos", userAgent)
	case "session.revoked_by_admin":
		d.raise(ctx, l, "session_revoked_by_admin", "low",
			"Un administrador cerro remotamente una sesion", userAgent)
	case "user.bulk_read":
		// El gateway detecto un volumen inusual de lecturas/descargas del usuario:
		// la firma de una cuenta comprometida extrayendo datos en masa.
		d.raise(ctx, l, "bulk_exfiltration", "high",
			"Volumen inusual de lecturas o descargas: posible extraccion masiva de datos", userAgent)
	}
}

func (d *SecurityDetector) inspectLogin(ctx context.Context, l *domain.AuditLog, userAgent string) {
	if l.UserID == uuid.Nil || !realIP(l.IPAddress) {
		return
	}
	// IP nueva: el usuario ya tenia historial y nunca habia entrado desde esta IP.
	// El registro recien insertado se excluye para no contarse a si mismo.
	known, err := d.logs.HasUserActionFromIP(ctx, l.TenantID, l.UserID, "user.logged_in", l.IPAddress, l.ID)
	if err != nil {
		d.logger.Warn("detector: historial de IP", zap.Error(err))
		return
	}
	agents, err := d.logs.ListUserActionAgents(ctx, l.TenantID, l.UserID, "user.logged_in", l.ID, 100)
	if err != nil {
		d.logger.Warn("detector: historial de agentes", zap.Error(err))
		return
	}
	hasHistory := len(agents) > 0
	if !known && hasHistory {
		d.raise(ctx, l, "login_new_ip", "medium",
			"Inicio de sesion desde una IP nunca vista para este usuario", userAgent)
	}
	if userAgent != "" && hasHistory && !knownDeviceFamily(agents, userAgent) {
		d.raise(ctx, l, "login_new_device", "low",
			"Inicio de sesion desde un dispositivo nuevo: "+deviceFamily(userAgent), userAgent)
	}

	// Viaje imposible: el mismo usuario inicio sesion desde OTRA IP real hace muy
	// poco. Sin geolocalizacion, la ventana corta (10 min) es el proxy: dos
	// ubicaciones distintas en ese lapso no son fisicamente compatibles y delatan
	// una sesion paralela (credenciales robadas). Riesgo alto: avisa al usuario y a
	// los administradores.
	other, err := d.logs.RecentLoginOtherIP(ctx, l.TenantID, l.UserID, l.IPAddress, time.Now().Add(-10*time.Minute), l.ID)
	if err != nil {
		d.logger.Warn("detector: viaje imposible", zap.Error(err))
		return
	}
	if realIP(other) {
		d.raise(ctx, l, "impossible_travel", "high",
			"Inicio de sesion desde dos IPs distintas en pocos minutos: "+other+" y "+l.IPAddress, userAgent)
	}
}

func (d *SecurityDetector) inspectFailedLogin(ctx context.Context, l *domain.AuditLog) {
	if !realIP(l.IPAddress) {
		return
	}
	since := time.Now().Add(-d.cfg.BruteForceWindow)
	n, err := d.logs.CountRecentByActionIP(ctx, l.TenantID, "user.login_failed", l.IPAddress, since)
	if err != nil {
		d.logger.Warn("detector: conteo de fallos", zap.Error(err))
		return
	}
	if n < d.cfg.BruteForceMax {
		return
	}
	// Una alerta por ventana e IP, no una por intento.
	dup, err := d.security.HasRecentEvent(ctx, l.TenantID, "brute_force", l.IPAddress, since)
	if err != nil || dup {
		return
	}
	d.raise(ctx, l, "brute_force", "high",
		fmt.Sprintf("%d intentos fallidos de inicio de sesion desde la IP %s en %s",
			n, l.IPAddress, d.cfg.BruteForceWindow), "")
}

func (d *SecurityDetector) raise(ctx context.Context, l *domain.AuditLog, eventType, risk, detail, userAgent string) {
	evt := &domain.SecurityEvent{
		ID:        uuid.New(),
		TenantID:  l.TenantID,
		EventType: eventType,
		IPAddress: l.IPAddress,
		Detail:    detail,
		RiskLevel: risk,
		CreatedAt: time.Now().UTC(),
	}
	if l.UserID != uuid.Nil {
		uid := l.UserID
		evt.UserID = &uid
	}
	if userAgent != "" {
		evt.UserAgent = &userAgent
	}
	if err := d.security.Create(ctx, evt); err != nil {
		d.logger.Error("detector: crear evento de seguridad", zap.Error(err), zap.String("tipo", eventType))
		return
	}
	if err := d.events.PublishSecurityAlert(l.TenantID.String(), eventType, detail, risk, l.IPAddress, l.UserID.String()); err != nil {
		d.logger.Warn("detector: publicar alerta", zap.Error(err))
	}
}

// realIP descarta el marcador de ausencia y vacios: sin IP real no hay heuristica
// de red que valga.
func realIP(ip string) bool {
	return ip != "" && ip != "0.0.0.0"
}

// El ORDEN es la regla, no un detalle: Edge y Opera se anuncian tambien como Chrome,
// y Chrome como Safari. Buscar la primera coincidencia dentro del texto clasificaria
// a un usuario de Edge como Chrome, y entonces cambiar de navegador no levantaria la
// alerta de dispositivo nuevo. Se comprueba de lo mas especifico a lo mas generico.
var browserMarkers = []struct{ marker, name string }{
	{"Edg/", "Edge"},
	{"OPR/", "Opera"},
	{"Opera", "Opera"},
	{"Firefox/", "Firefox"},
	{"Chrome/", "Chrome"},
	{"Safari/", "Safari"},
	{"curl/", "curl"},
}

var osMarkers = []struct{ marker, name string }{
	{"Android", "Android"},
	{"iPhone", "iPhone"},
	{"iPad", "iPad"},
	{"Windows", "Windows"},
	{"Mac OS X", "Mac OS X"},
	{"Macintosh", "Mac OS X"},
	{"Linux", "Linux"},
}

// deviceFamily reduce el user-agent a "navegador+SO": comparar el UA crudo
// dispararia falsas alertas con cada actualizacion menor del navegador.
func deviceFamily(ua string) string {
	browser := "otro"
	for _, m := range browserMarkers {
		if strings.Contains(ua, m.marker) {
			browser = m.name
			break
		}
	}
	os := "desconocido"
	for _, m := range osMarkers {
		if strings.Contains(ua, m.marker) {
			os = m.name
			break
		}
	}
	return browser + " en " + os
}

func knownDeviceFamily(history []string, ua string) bool {
	family := deviceFamily(ua)
	for _, h := range history {
		if deviceFamily(h) == family {
			return true
		}
	}
	return false
}
