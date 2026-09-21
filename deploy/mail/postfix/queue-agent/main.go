// Command queue-agent expone la cola de Postfix del contenedor por HTTPS: listar los mensajes con su
// motivo de diferimiento, reintentar, retener, liberar y borrar uno por su identificador, y vaciar la
// cola diferida. Lo usa mail-security de la celda para el gestor de cola del superadmin.
//
// Corre dentro del contenedor de Postfix y como root, porque postsuper solo lo admite del superusuario.
// Por eso es deliberadamente pequeno: no ejecuta un shell, no admite mas argumentos que un identificador
// de cola validado y una de cinco acciones, no devuelve el contenido de ningun mensaje y solo atiende a
// quien presenta QUEUE_AGENT_API_KEY. Sin esa clave no abre el puerto (y no termina: supervisord
// detiene el contenedor si cualquiera de sus procesos sale).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultListen = ":8590"
	defaultCert   = "/etc/ssl/mail/cert.pem"
	defaultKey    = "/etc/ssl/mail/key.pem"
	minKeyLength  = 32
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", "queue-agent")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	key := strings.TrimSpace(os.Getenv("QUEUE_AGENT_API_KEY"))
	if key == "" {
		log.Warn("gestor de cola desactivado: falta QUEUE_AGENT_API_KEY")
		<-ctx.Done()
		return
	}
	if len(key) < minKeyLength {
		log.Error("QUEUE_AGENT_API_KEY es demasiado corta", "minimo", minKeyLength)
		<-ctx.Done()
		return
	}

	certs := &certReloader{cert: envOr("QUEUE_AGENT_TLS_CERT", defaultCert), key: envOr("QUEUE_AGENT_TLS_KEY", defaultKey)}
	if _, err := certs.get(nil); err != nil {
		log.Error("no se pudo leer el certificado", "error", err)
		<-ctx.Done()
		return
	}
	srv := &http.Server{
		Addr:              envOr("QUEUE_AGENT_LISTEN", defaultListen),
		Handler:           NewServer(NewQueue(), key, log).Routes(),
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.get},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("gestor de cola escuchando", "addr", srv.Addr)
	if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("el servidor termino", "error", err)
		<-ctx.Done()
	}
}

func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// certReloader relee el certificado cuando acme lo renueva, sin reiniciar el proceso.
type certReloader struct {
	cert, key string
	mu        sync.Mutex
	loaded    time.Time
	current   *tls.Certificate
}

const certRecheck = time.Minute

func (c *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil && time.Since(c.loaded) < certRecheck {
		return c.current, nil
	}
	pair, err := tls.LoadX509KeyPair(c.cert, c.key)
	if err != nil {
		if c.current != nil {
			return c.current, nil
		}
		return nil, err
	}
	c.current, c.loaded = &pair, time.Now()
	return c.current, nil
}
