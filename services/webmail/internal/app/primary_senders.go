package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	"go.uber.org/zap"
)

const (
	// inboxFolder es la unica carpeta en la que la pestana de la bandeja inteligente tiene sentido.
	inboxFolder = "INBOX"
	// primarySendersTTL es lo que tarda en notarse, en otra replica del webmail, un remitente fijado o
	// quitado. La lista se pide en cada listado de la bandeja y en cada lectura, y el directorio
	// limita las peticiones internas por origen: sin cache, la bandeja se la gastaria entera.
	primarySendersTTL = time.Minute
	// primarySendersFailureTTL es lo que se recuerda que el directorio no respondio, para no
	// insistirle en cada listado mientras esta caido o limitando.
	primarySendersFailureTTL = 15 * time.Second
	// maxPrimarySendersCacheEntries acota la cache; al llenarse se vacia entera, que solo cuesta una
	// consulta por buzon activo.
	maxPrimarySendersCacheEntries = 10000
)

type primarySendersEntry struct {
	addresses []string
	failed    bool
	expires   time.Time
}

// primarySendersCache guarda por buzon sus remitentes fijados, con vida corta. Las altas y bajas
// hechas desde esta replica la invalidan al momento.
type primarySendersCache struct {
	mu      sync.Mutex
	entries map[string]primarySendersEntry
}

func (c *primarySendersCache) get(username string, now time.Time) (primarySendersEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[username]
	if !ok || !now.Before(e.expires) {
		return primarySendersEntry{}, false
	}
	return e, true
}

func (c *primarySendersCache) put(username string, e primarySendersEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= maxPrimarySendersCacheEntries {
		c.entries = map[string]primarySendersEntry{}
	}
	c.entries[username] = e
}

func (c *primarySendersCache) drop(username string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, username)
}

// cachedPrimarySenders lee la lista del buzon, del directorio solo si no esta en cache. Un fallo se
// recuerda unos segundos y se devuelve a quien lo pide.
func (s *Service) cachedPrimarySenders(ctx context.Context, username string) ([]string, error) {
	now := s.clock()
	if e, ok := s.primaryCache.get(username, now); ok {
		if e.failed {
			return nil, errPrimarySendersRecentlyFailed
		}
		return e.addresses, nil
	}
	list, err := s.primarySenders.PrimarySenders(ctx, username)
	if err != nil {
		s.primaryCache.put(username, primarySendersEntry{failed: true, expires: now.Add(primarySendersFailureTTL)})
		return nil, err
	}
	s.primaryCache.put(username, primarySendersEntry{addresses: list, expires: now.Add(primarySendersTTL)})
	return list, nil
}

// errPrimarySendersRecentlyFailed repite sin volver a preguntar un fallo del directorio de hace segundos.
var errPrimarySendersRecentlyFailed = errors.New("el directorio no respondio hace unos segundos")

// PrimarySenders devuelve las direcciones que el buzon fijo en Principal.
func (s *Service) PrimarySenders(ctx context.Context, sess domain.Session) ([]string, error) {
	list, err := s.cachedPrimarySenders(ctx, sess.Username)
	if err != nil {
		s.logFailure("no se pudieron leer los remitentes fijados en Principal", sess, err)
		return nil, unavailable(err)
	}
	return list, nil
}

// AddPrimarySender fija una direccion en Principal: sus mensajes caeran siempre en esa pestana.
func (s *Service) AddPrimarySender(ctx context.Context, sess domain.Session, address string) error {
	err := s.primarySenders.AddPrimarySender(ctx, sess.Username, address)
	s.primaryCache.drop(sess.Username)
	return s.primarySenderResult("no se pudo fijar el remitente en Principal", sess, err)
}

// RemovePrimarySender devuelve una direccion a las reglas normales de la bandeja.
func (s *Service) RemovePrimarySender(ctx context.Context, sess domain.Session, address string) error {
	err := s.primarySenders.RemovePrimarySender(ctx, sess.Username, address)
	s.primaryCache.drop(sess.Username)
	return s.primarySenderResult("no se pudo quitar el remitente de Principal", sess, err)
}

func (s *Service) primarySenderResult(msg string, sess domain.Session, err error) error {
	if err == nil {
		return nil
	}
	var invalid *domain.ValidationError
	if errors.Is(err, domain.ErrPrimarySenderLimit) || errors.As(err, &invalid) {
		return err
	}
	s.logFailure(msg, sess, err)
	return unavailable(err)
}

// primarySendersFor son los remitentes fijados que clasifican la bandeja de entrada. Si el directorio
// no responde se lista igual, sin ellos: la bandeja no depende de un ajuste de comodidad.
func (s *Service) primarySendersFor(ctx context.Context, sess domain.Session, folder string) []string {
	// Sin pestanas no hay nada que fijar: no se consulta al directorio.
	if len(s.cfg.InboxCategories) == 0 || !strings.EqualFold(folder, inboxFolder) {
		return nil
	}
	list, err := s.cachedPrimarySenders(ctx, sess.Username)
	if err != nil {
		s.logger.Warn("webmail: la bandeja se lista sin los remitentes fijados en Principal",
			zap.String("username", sess.Username), zap.Error(err))
		return nil
	}
	return list
}

// classifierFor es el clasificador del buzon de la sesion con sus remitentes fijados.
func (s *Service) classifierFor(ctx context.Context, sess domain.Session, folder string) domain.Classifier {
	return domain.ClassifierFor(sess.Username).WithPrimarySenders(s.primarySendersFor(ctx, sess, folder))
}
