// Package resetqueue atiende fuera de la peticion las solicitudes de "olvide mi contrasena": una
// cola acotada en memoria con sus propios trabajadores. La peticion solo encola, exista o no la
// cuenta, y responde; buscar la cuenta, guardar el enlace y llamar a transactional ocurre
// despues, donde su tiempo ya no llega a quien pregunta.
//
// No pasa por la outbox: no hay un cambio de negocio con el que confirmarse (con un correo
// desconocido no se escribe nada) y la outbox y el stream guardarian durante dias cada correo
// que alguien teclea. Lo que se pierde en una caida es una solicitud que el usuario repite; la
// respuesta nunca prometio el envio.
package resetqueue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
	"go.uber.org/zap"
)

// Options dimensiona la cola. Todas son obligatorias.
type Options struct {
	// Capacity acota las solicitudes en espera; con la cola llena, Enqueue descarta.
	Capacity int
	// Workers es cuantas solicitudes se atienden a la vez.
	Workers int
	// JobTimeout es el plazo de cada solicitud.
	JobTimeout time.Duration
	// DrainTimeout es cuanto se siguen atendiendo las solicitudes encoladas tras la orden de
	// parar; las que quedan despues se descartan.
	DrainTimeout time.Duration
}

// Handler atiende una solicitud dentro del plazo de ctx.
type Handler func(ctx context.Context, req ports.PasswordResetRequest)

type Queue struct {
	jobs   chan ports.PasswordResetRequest
	mu     sync.RWMutex
	closed bool
	opt    Options
	logger *zap.Logger
}

func New(opt Options, logger *zap.Logger) (*Queue, error) {
	if opt.Capacity <= 0 || opt.Workers <= 0 || opt.JobTimeout <= 0 || opt.DrainTimeout <= 0 {
		return nil, fmt.Errorf("resetqueue: capacidad, trabajadores y plazos deben ser positivos: %+v", opt)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Queue{jobs: make(chan ports.PasswordResetRequest, opt.Capacity), opt: opt, logger: logger}, nil
}

// Enqueue no espera nunca: con la cola llena o cerrada devuelve false. El cerrojo de lectura
// impide enviar a un canal que Run acaba de cerrar.
func (q *Queue) Enqueue(req ports.PasswordResetRequest) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return false
	}
	select {
	case q.jobs <- req:
		return true
	default:
		return false
	}
}

// Run atiende la cola hasta que ctx termina; entonces deja de aceptar, atiende lo encolado
// durante DrainTimeout y vuelve cuando todos los trabajadores han terminado. Cada solicitud corre
// con un contexto propio, que no depende de la peticion que la encolo. Se llama una sola vez.
func (q *Queue) Run(ctx context.Context, handle Handler) {
	drain, stop := context.WithCancel(context.WithoutCancel(ctx))
	defer stop()
	go func() {
		<-ctx.Done()
		q.close()
		timer := time.AfterFunc(q.opt.DrainTimeout, stop)
		<-drain.Done()
		timer.Stop()
	}()

	var wg sync.WaitGroup
	var dropped atomic.Int64
	for range q.opt.Workers {
		wg.Go(func() {
			for req := range q.jobs {
				if drain.Err() != nil {
					dropped.Add(1)
					continue
				}
				q.serve(drain, req, handle)
			}
		})
	}
	wg.Wait()
	if n := dropped.Load(); n > 0 {
		q.logger.Warn("reinicio de contrasena: solicitudes sin atender al apagar", zap.Int64("solicitudes", n))
	}
}

// serve atiende una solicitud. Un panico se registra y no tumba al trabajador ni al servicio,
// igual que el servidor HTTP con una peticion.
func (q *Queue) serve(parent context.Context, req ports.PasswordResetRequest, handle Handler) {
	ctx, cancel := context.WithTimeout(parent, q.opt.JobTimeout)
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			q.logger.Error("reinicio de contrasena: la solicitud fallo con un panico", zap.Any("panico", r), zap.Stack("pila"))
		}
	}()
	handle(ctx, req)
}

func (q *Queue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.jobs)
	}
}

var _ ports.PasswordResetQueue = (*Queue)(nil)
