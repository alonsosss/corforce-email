package imap

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"go.uber.org/zap"
)

const (
	watchedFolder = "INBOX"
	// idleGrace es cuanto sigue vigilando un buzon sin nadie suscrito: la interfaz reconecta su flujo cada
	// pocos segundos por el limite del gateway, y sin este margen cada reconexion abriria otra sesion IMAP.
	idleGrace        = 30 * time.Second
	subscriberBuffer = 1
	backoffMin       = 2 * time.Second
	backoffMax       = time.Minute
)

// WatchConfig acota la vigilancia. Sin topes, un buzon con muchas pestanas o un proceso con muchos buzones
// abriria una sesion IMAP por cada una.
type WatchConfig struct {
	MaxPerMailbox int
	MaxMailboxes  int
}

// idleSource vigila la bandeja del buzon hasta que ctx se cancele o falle, y llama a notify por cada cambio.
type idleSource func(ctx context.Context, username string, notify func(messages uint32)) error

// Watcher implementa ports.MailboxWatcher: una sesion IMAP en IDLE por buzon, compartida.
type Watcher struct {
	cfg    WatchConfig
	source idleSource
	logger *zap.Logger
	// grace y retryMin son las constantes de siempre; las pruebas los acortan.
	grace    time.Duration
	retryMin time.Duration

	mu      sync.Mutex
	watches map[string]*watch
}

type watch struct {
	cancel context.CancelFunc
	subs   map[int]chan domain.MailboxChange
	nextID int
	// idleTimer arranca cuando el ultimo suscrito se va y se detiene si vuelve alguien.
	idleTimer *time.Timer
}

// NewWatcher vigila con el IDLE de Dovecot a traves del Store.
func NewWatcher(store *Store, cfg WatchConfig, logger *zap.Logger) *Watcher {
	return newWatcher(store.idleSource, cfg, logger)
}

func newWatcher(source idleSource, cfg WatchConfig, logger *zap.Logger) *Watcher {
	return &Watcher{cfg: cfg, source: source, logger: logger, grace: idleGrace, retryMin: backoffMin, watches: map[string]*watch{}}
}

func (w *Watcher) Watch(ctx context.Context, username string) (<-chan domain.MailboxChange, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wt := w.watches[username]
	if wt == nil {
		if len(w.watches) >= w.cfg.MaxMailboxes {
			return nil, domain.ErrTooManyStreams
		}
		wt = w.start(username)
	}
	if len(wt.subs) >= w.cfg.MaxPerMailbox {
		return nil, domain.ErrTooManyStreams
	}
	if wt.idleTimer != nil {
		wt.idleTimer.Stop()
		wt.idleTimer = nil
	}
	id := wt.nextID
	wt.nextID++
	ch := make(chan domain.MailboxChange, subscriberBuffer)
	wt.subs[id] = ch
	go func() {
		<-ctx.Done()
		w.unsubscribe(username, wt, id)
	}()
	return ch, nil
}

func (w *Watcher) unsubscribe(username string, wt *watch, id int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ch, ok := wt.subs[id]; ok {
		delete(wt.subs, id)
		close(ch)
	}
	if len(wt.subs) == 0 && wt.idleTimer == nil {
		wt.idleTimer = time.AfterFunc(w.grace, func() { w.retire(username, wt) })
	}
}

// retire detiene la vigilancia de un buzon que sigue sin suscritos tras el margen.
func (w *Watcher) retire(username string, wt *watch) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(wt.subs) != 0 || w.watches[username] != wt {
		return
	}
	delete(w.watches, username)
	wt.cancel()
}

// start crea la vigilancia con w.mu tomado.
func (w *Watcher) start(username string) *watch {
	ctx, cancel := context.WithCancel(context.Background())
	wt := &watch{cancel: cancel, subs: map[int]chan domain.MailboxChange{}}
	w.watches[username] = wt
	go w.run(ctx, username, wt)
	return wt
}

// run mantiene la sesion IDLE: si cae, reconecta con espera creciente y aleatoria.
func (w *Watcher) run(ctx context.Context, username string, wt *watch) {
	backoff := w.retryMin
	for ctx.Err() == nil {
		started := time.Now()
		err := w.source(ctx, username, func(messages uint32) { w.fanOut(wt, domain.MailboxChange{Messages: messages}) })
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > backoffMax {
			backoff = w.retryMin
		}
		w.logger.Warn("webmail: la vigilancia de la bandeja se corto; se reintenta", zap.String("username", username), zap.Duration("espera", backoff), zap.Error(err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff/2 + rand.N(backoff/2+1)):
		}
		backoff = min(backoff*2, backoffMax)
	}
}

// fanOut avisa a todos los suscritos sin esperar a ninguno: el canal guarda un solo aviso, y uno pendiente
// ya basta para que la interfaz vuelva a leer.
func (w *Watcher) fanOut(wt *watch, change domain.MailboxChange) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, ch := range wt.subs {
		select {
		case ch <- change:
		default:
		}
	}
}

// idleSource es la vigilancia real: sesion IMAP como el buzon, INBOX en solo lectura y IDLE.
func (s *Store) idleSource(ctx context.Context, username string, notify func(messages uint32)) error {
	handler := &imapclient.UnilateralDataHandler{
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil {
				notify(*data.NumMessages)
			}
		},
		Expunge: func(uint32) { notify(0) },
		Fetch: func(msg *imapclient.FetchMessageData) {
			// El aviso de banderas trae el mensaje por cursor: hay que consumirlo para no bloquear al cliente.
			_, _ = msg.Collect()
			notify(0)
		},
	}
	c, err := s.dial(ctx, username, handler)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	defer func() { _ = c.Close() }()

	if _, err := c.Select(watchedFolder, &imaplib.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return mapError(err, domain.ErrFolderNotFound)
	}
	idle, err := c.Idle()
	if err != nil {
		return unavailable("IDLE", err)
	}
	// Wait vuelve cuando el servidor cierra la conexion o el contexto la cierra.
	if err := idle.Wait(); err != nil && ctx.Err() == nil {
		return unavailable("IDLE", err)
	}
	return nil
}
