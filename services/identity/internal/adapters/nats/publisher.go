package nats

import (
	"github.com/alonsosss/corforce-email/pkg/events"
)

type EventPublisher struct {
	bus *events.Bus
}

func NewEventPublisher(bus *events.Bus) *EventPublisher {
	return &EventPublisher{bus: bus}
}

func (p *EventPublisher) PublishUserCreated(tenantID, userID, email string) error {
	return p.bus.Publish("identity.user.created", events.Event{
		Type:     "user.created",
		Source:   "identity-service",
		TenantID: tenantID,
		UserID:   userID,
		Data:     map[string]string{"email": email},
	})
}

func (p *EventPublisher) PublishUserLoggedIn(tenantID, userID, ip, userAgent string) error {
	return p.bus.Publish("identity.user.logged_in", events.Event{
		Type:     "user.logged_in",
		Source:   "identity-service",
		TenantID: tenantID,
		UserID:   userID,
		Data:     map[string]string{"ip": ip, "user_agent": userAgent},
	})
}

func (p *EventPublisher) PublishLoginFailed(tenantID, userID, email, ip, userAgent string) error {
	return p.bus.Publish("identity.user.login_failed", events.Event{
		Type:     "user.login_failed",
		Source:   "identity-service",
		TenantID: tenantID,
		UserID:   userID,
		Data:     map[string]string{"email": email, "ip": ip, "user_agent": userAgent},
	})
}

func (p *EventPublisher) PublishSessionRevoked(tenantID, actorID, targetUserID, sessionID, ip string) error {
	return p.bus.Publish("identity.session.revoked_by_admin", events.Event{
		Type:     "session.revoked_by_admin",
		Source:   "identity-service",
		TenantID: tenantID,
		UserID:   actorID,
		Data:     map[string]string{"target_user_id": targetUserID, "session_id": sessionID, "ip": ip},
	})
}

func (p *EventPublisher) PublishUserLoggedOut(tenantID, userID string) error {
	return p.bus.Publish("identity.user.logged_out", events.Event{
		Type:     "user.logged_out",
		Source:   "identity-service",
		TenantID: tenantID,
		UserID:   userID,
	})
}

func (p *EventPublisher) PublishUserLocked(tenantID, userID string) error {
	return p.bus.Publish("identity.user.locked", events.Event{
		Type:     "user.locked",
		Source:   "identity-service",
		TenantID: tenantID,
		UserID:   userID,
	})
}

func (p *EventPublisher) PublishPasswordChanged(tenantID, userID string) error {
	return p.bus.Publish("identity.user.password_changed", events.Event{
		Type:     "user.password_changed",
		Source:   "identity-service",
		TenantID: tenantID,
		UserID:   userID,
	})
}
