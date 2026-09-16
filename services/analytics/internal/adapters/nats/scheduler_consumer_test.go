package nats

import (
	"errors"
	"fmt"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/events"
	"github.com/alonsosss/corforce-email/services/analytics/internal/ports"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func jobEvent(tenantEnvelope string, data map[string]interface{}) events.Event {
	return events.Event{ID: uuid.NewString(), Type: "scheduler.job.started", TenantID: tenantEnvelope, Data: data}
}

func TestUnTrabajoDeOtroManejadorNoEsDeEsteServicio(t *testing.T) {
	evt := jobEvent(uuid.NewString(), map[string]interface{}{
		"handler": "otro.servicio.tarea", "execution_id": uuid.NewString(),
	})
	if _, err := parseSchedulerJob(evt); !errors.Is(err, errNotMine) {
		t.Fatalf("%v, se esperaba errNotMine", err)
	}
	task := events.Event{Type: "scheduler.task.started", TenantID: uuid.NewString(),
		Data: map[string]interface{}{"handler": "otro.servicio.tarea"}}
	if _, err := parseSchedulerTask(task); !errors.Is(err, errNotMine) {
		t.Fatalf("tarea: %v, se esperaba errNotMine", err)
	}
}

// Un trabajo de plataforma lleva data.tenant_id nulo: la empresa es la del sobre, que es la
// de la base en la que vive la ejecucion y la que hay que devolver al cerrarla.
func TestLaEmpresaDeUnTrabajoDePlataformaSaleDelSobre(t *testing.T) {
	tenant, execution := uuid.New(), uuid.New()
	evt := jobEvent(tenant.String(), map[string]interface{}{
		"handler": handlerRetentionPrune, "execution_id": execution.String(), "tenant_id": nil,
	})
	job, err := parseSchedulerJob(evt)
	if err != nil {
		t.Fatal(err)
	}
	if job.tenantID != tenant || job.executionID != execution {
		t.Fatalf("empresa %s y ejecucion %s", job.tenantID, job.executionID)
	}
}

func TestLaEmpresaDeUnTrabajoPropioSaleDelPayload(t *testing.T) {
	own, envelope, execution := uuid.New(), uuid.New(), uuid.New()
	evt := jobEvent(envelope.String(), map[string]interface{}{
		"handler": handlerRetentionPrune, "execution_id": execution.String(), "tenant_id": own.String(),
	})
	job, err := parseSchedulerJob(evt)
	if err != nil {
		t.Fatal(err)
	}
	if job.tenantID != own {
		t.Fatalf("empresa: %s, se esperaba la del payload %s", job.tenantID, own)
	}
}

func TestUnDespachoSinLoQueHaceFaltaEsIlegible(t *testing.T) {
	tenant := uuid.NewString()
	cases := map[string]events.Event{
		"sin execution_id": jobEvent(tenant, map[string]interface{}{"handler": handlerRetentionPrune}),
		"execution_id que no es uuid": jobEvent(tenant, map[string]interface{}{
			"handler": handlerRetentionPrune, "execution_id": "no-es-un-uuid"}),
		"sin empresa en ningun sitio": jobEvent("", map[string]interface{}{
			"handler": handlerRetentionPrune, "execution_id": uuid.NewString()}),
		"empresa vacia": jobEvent(uuid.Nil.String(), map[string]interface{}{
			"handler": handlerRetentionPrune, "execution_id": uuid.NewString(), "tenant_id": uuid.Nil.String()}),
	}
	for name, evt := range cases {
		if _, err := parseSchedulerJob(evt); !errors.Is(err, errMalformed) {
			t.Errorf("%s: %v, se esperaba errMalformed", name, err)
		}
	}
	// Un evento sin datos no nombra ningun manejador: no es de este servicio, y eso no es un
	// error que haya que registrar.
	if _, err := parseSchedulerJob(jobEvent(tenant, nil)); !errors.Is(err, errNotMine) {
		t.Errorf("sin datos: %v, se esperaba errNotMine", err)
	}
}

// ack se llama solo cuando el cierre quedo registrado o cuando el scheduler lo rechazo de
// forma definitiva: mientras no responda, el mensaje se reentrega.
func TestElCierreSoloSeConfirmaCuandoElSchedulerRespondio(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	c := &Consumers{logger: zap.New(core)}
	cases := []struct {
		nombre   string
		err      error
		confirma bool
	}{
		{"registrado", nil, true},
		{"rechazado de forma definitiva", fmt.Errorf("%w: status 409 CONFLICT", ports.ErrReportRejected), true},
		{"el scheduler no responde", fmt.Errorf("%w: connection refused", ports.ErrSchedulerUnavailable), false},
	}
	for _, c2 := range cases {
		acked := 0
		c.close(events.Event{ID: uuid.NewString()}, func() { acked++ }, c2.err)
		if (acked == 1) != c2.confirma {
			t.Errorf("%s: confirmaciones %d, se esperaba confirma=%v", c2.nombre, acked, c2.confirma)
		}
	}
	if len(logs.All()) != 2 {
		t.Fatalf("los dos casos con error quedan registrados: %d", len(logs.All()))
	}
}

// El despacho de otro manejador no es un problema: todos los ejecutores del catalogo reciben
// el mismo subject. Uno ilegible si se registra.
func TestSoloSeRegistraElDespachoIlegible(t *testing.T) {
	core, logs := observer.New(zap.ErrorLevel)
	c := &Consumers{logger: zap.New(core)}
	c.skip(events.Event{ID: uuid.NewString()}, errNotMine)
	if len(logs.All()) != 0 {
		t.Fatal("el manejador ajeno no se registra")
	}
	c.skip(events.Event{ID: uuid.NewString()}, errMalformed)
	if len(logs.All()) != 1 {
		t.Fatalf("el ilegible si: %d", len(logs.All()))
	}
}
