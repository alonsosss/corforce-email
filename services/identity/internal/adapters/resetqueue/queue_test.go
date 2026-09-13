package resetqueue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alonsosss/corforce-email/services/identity/internal/ports"
)

// waitFor falla si fn no vuelve dentro del plazo: una cola que no termina no cuelga la prueba.
func waitFor(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s no termino", what)
	}
}

func run(q *Queue, ctx context.Context, h Handler) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Run(ctx, h)
	}()
	return done
}

func req(email string) ports.PasswordResetRequest {
	return ports.PasswordResetRequest{Email: email, IPAddress: "203.0.113.7"}
}

func TestNewRechazaOpcionesSinValor(t *testing.T) {
	valid := Options{Capacity: 1, Workers: 1, JobTimeout: time.Second, DrainTimeout: time.Second}
	for name, mutate := range map[string]func(*Options){
		"capacidad":       func(o *Options) { o.Capacity = 0 },
		"trabajadores":    func(o *Options) { o.Workers = -1 },
		"plazo":           func(o *Options) { o.JobTimeout = 0 },
		"plazo de parada": func(o *Options) { o.DrainTimeout = 0 },
	} {
		o := valid
		mutate(&o)
		if _, err := New(o, nil); err == nil {
			t.Errorf("sin %s la cola no debe crearse", name)
		}
	}
}

// Encolar nunca espera: con la cola llena se descarta al momento.
func TestEncolarNoEsperaConLaColaLlena(t *testing.T) {
	q, err := New(Options{Capacity: 2, Workers: 1, JobTimeout: time.Second, DrainTimeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Enqueue(req("a@example.test")) || !q.Enqueue(req("b@example.test")) {
		t.Fatal("la cola no admite su capacidad")
	}
	if q.Enqueue(req("c@example.test")) {
		t.Fatal("la cola llena admitio otra solicitud")
	}
}

// Cada solicitud se atiende con un contexto propio, con su plazo y vivo aunque la peticion que
// la encolo ya haya respondido. Parada la cola, no admite mas.
func TestAtiendeCadaSolicitudConSuPlazo(t *testing.T) {
	q, err := New(Options{Capacity: 8, Workers: 2, JobTimeout: time.Minute, DrainTimeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	type seen struct {
		email    string
		deadline bool
		err      error
	}
	got := make(chan seen, 8)
	ctx, stop := context.WithCancel(context.Background())
	done := run(q, ctx, func(jctx context.Context, r ports.PasswordResetRequest) {
		_, deadline := jctx.Deadline()
		got <- seen{r.Email, deadline, jctx.Err()}
	})
	emails := map[string]bool{"a@example.test": true, "b@example.test": true, "c@example.test": true}
	for e := range emails {
		if !q.Enqueue(req(e)) {
			t.Fatal("no se pudo encolar")
		}
	}
	for range emails {
		select {
		case s := <-got:
			if !emails[s.email] || !s.deadline || s.err != nil {
				t.Errorf("solicitud %+v", s)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("la cola no atendio lo encolado")
		}
	}
	stop()
	waitFor(t, "la cola", done)
	if q.Enqueue(req("d@example.test")) {
		t.Fatal("la cola parada admitio una solicitud")
	}
}

// Al parar se atiende lo que ya estaba encolado.
func TestAlPararAtiendeLoEncolado(t *testing.T) {
	q, err := New(Options{Capacity: 8, Workers: 1, JobTimeout: time.Minute, DrainTimeout: 10 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"a", "b", "c", "d", "e"} {
		q.Enqueue(req(e + "@example.test"))
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	var handled atomic.Int32
	waitFor(t, "la cola", run(q, ctx, func(context.Context, ports.PasswordResetRequest) { handled.Add(1) }))
	if handled.Load() != 5 {
		t.Fatalf("atendidas %d de 5", handled.Load())
	}
}

// Pasado el plazo de parada, la solicitud en curso ve su contexto cancelado y las que quedan se
// descartan: la parada no espera sin limite a transactional.
func TestElPlazoDeParadaCortaLoPendiente(t *testing.T) {
	q, err := New(Options{Capacity: 4, Workers: 1, JobTimeout: time.Minute, DrainTimeout: 50 * time.Millisecond}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"a", "b", "c"} {
		q.Enqueue(req(e + "@example.test"))
	}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	var handled atomic.Int32
	waitFor(t, "la cola", run(q, ctx, func(jctx context.Context, _ ports.PasswordResetRequest) {
		handled.Add(1)
		<-jctx.Done()
	}))
	if handled.Load() != 1 {
		t.Fatalf("atendidas %d, se esperaba solo la que estaba en curso", handled.Load())
	}
}

// Un panico en una solicitud no deja al trabajador sin atender las siguientes.
func TestUnPanicoNoDetieneAlTrabajador(t *testing.T) {
	q, err := New(Options{Capacity: 4, Workers: 1, JobTimeout: time.Minute, DrainTimeout: 10 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	q.Enqueue(req("panico@example.test"))
	q.Enqueue(req("sigue@example.test"))
	ctx, stop := context.WithCancel(context.Background())
	stop()
	var mu sync.Mutex
	var served []string
	waitFor(t, "la cola", run(q, ctx, func(_ context.Context, r ports.PasswordResetRequest) {
		if r.Email == "panico@example.test" {
			panic("fallo de prueba")
		}
		mu.Lock()
		served = append(served, r.Email)
		mu.Unlock()
	}))
	if len(served) != 1 || served[0] != "sigue@example.test" {
		t.Fatalf("atendidas tras el panico: %v", served)
	}
}
