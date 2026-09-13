package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const tag = "`"

// alphaFiles publica por la outbox de tres formas: subject constante con struct como
// payload, subject de otro paquete con un mapa al que el reenviador anade tenant_id, y
// el puerto Publish(ctx, subject, tenant, payload) con un payload armado por una funcion.
var alphaFiles = map[string]string{
	"services/alpha/internal/domain/events.go": `package domain

const SubjectItemRemoved = "alpha.item.removed"
`,
	"services/alpha/internal/adapters/outbox/publisher.go": `package outbox

import (
	"context"

	"example.com/fixture/pkg/events"
	"example.com/fixture/pkg/jobs"
	"example.com/fixture/pkg/outbox"
	"example.com/fixture/services/alpha/internal/domain"
)

const SubjectItemCreated = "alpha.item.created"

type itemPayload struct {
	TenantID string ` + tag + `json:"tenant_id"` + tag + `
	ItemID   string ` + tag + `json:"item_id"` + tag + `
	Note     string ` + tag + `json:"note,omitempty"` + tag + `
	Secret   string ` + tag + `json:"-"` + tag + `
	internal string
}

type Publisher struct{ q outbox.Execer }

func (p *Publisher) ItemCreated(ctx context.Context, tenantID, itemID string) error {
	return outbox.Enqueue(ctx, p.q, SubjectItemCreated, events.Event{
		TenantID: tenantID,
		Data:     itemPayload{TenantID: tenantID, ItemID: itemID},
	})
}

func (p *Publisher) ItemRemoved(ctx context.Context, tenantID, email string) error {
	return p.enqueue(ctx, domain.SubjectItemRemoved, tenantID, map[string]any{"email": email})
}

func (p *Publisher) enqueue(ctx context.Context, subject, tenantID string, data map[string]any) error {
	data["tenant_id"] = tenantID
	return outbox.Enqueue(ctx, p.q, subject, events.Event{TenantID: tenantID, Data: data})
}

// Otra cola con un Enqueue de la misma forma no es una publicacion.
func (p *Publisher) Job(ctx context.Context) error {
	return jobs.Enqueue(ctx, p.q, "alpha.job.ignored", nil)
}
`,
	"services/alpha/internal/adapters/postgres/outbox.go": `package postgres

import (
	"context"

	"example.com/fixture/pkg/events"
	"example.com/fixture/pkg/outbox"
)

type OutboxPublisher struct{ pool outbox.Execer }

func (p *OutboxPublisher) Publish(ctx context.Context, subject, tenantID string, payload map[string]any) error {
	return outbox.Enqueue(ctx, p.pool, subject, events.Event{Type: subject, TenantID: tenantID, Data: payload})
}
`,
	"services/alpha/internal/app/events.go": `package app

import "context"

type EventPublisher interface {
	Publish(ctx context.Context, subject, tenantID string, payload map[string]any) error
}

type UseCase struct{ events EventPublisher }

func runPayload(runID, reason string) map[string]any {
	p := map[string]any{"run_id": runID}
	if reason != "" {
		p["reason"] = reason
	}
	return p
}

func (uc *UseCase) runFailed(ctx context.Context, tenantID, runID, reason string) error {
	return uc.events.Publish(ctx, "alpha.run.failed", tenantID, runPayload(runID, reason))
}
`,
}

// betaFiles consume lo que alpha encola: una tabla de suscripciones con un handler
// fabricado por subject que lee campos distintos en cada rama, y un handler directo que
// lee un campo que alpha no publica.
var betaFiles = map[string]string{
	"services/beta/internal/app/subjects.go": `package app

const (
	SubjectItemCreated = "alpha.item.created"
	SubjectItemRemoved = "alpha.item.removed"
)
`,
	"services/beta/internal/adapters/nats/worker.go": `package nats

import (
	"example.com/fixture/pkg/events"
	"example.com/fixture/services/beta/internal/app"
)

type subscription struct {
	subject string
	durable string
}

type Worker struct{ bus *events.Bus }

func (w *Worker) Start() {
	w.subscribeAll([]subscription{
		{subject: app.SubjectItemCreated, durable: "beta-created"},
		{subject: app.SubjectItemRemoved, durable: "beta-removed"},
	})
	_, _ = w.bus.DurableQueueSubscribe("alpha.run.failed", "beta-runs", w.onRunFailed)
	_, _ = w.bus.Subscribe("alpha.item.*", func(evt events.Event) {})
}

func (w *Worker) subscribeAll(pending []subscription) {
	for _, s := range pending {
		_, _ = w.bus.DurableQueueSubscribe(s.subject, s.durable, w.handle(s.subject))
	}
}

func (w *Worker) handle(subject string) func(events.Event, func()) {
	return func(evt events.Event, ack func()) {
		data, _ := evt.Data.(map[string]any)
		_ = data["tenant_id"]
		if subject == app.SubjectItemRemoved {
			_ = data["email"]
			ack()
			return
		}
		_ = data["item_id"]
		ack()
	}
}

func (w *Worker) onRunFailed(evt events.Event, ack func()) {
	data, _ := evt.Data.(map[string]any)
	_ = data["run_id"]
	_ = data["workflow_id"]
	ack()
}
`,
}

// gammaFiles tiene publicaciones cuyo subject no se puede fijar estaticamente.
var gammaFiles = map[string]string{
	"services/gamma/internal/adapters/outbox/publisher.go": `package outbox

import (
	"context"

	"example.com/fixture/pkg/events"
	"example.com/fixture/pkg/outbox"
)

type Publisher struct{ q outbox.Execer }

type row struct{ Subject string }

func (p *Publisher) Emit(ctx context.Context, kinds []string) error {
	for _, kind := range kinds {
		subject := "gamma." + kind + ".created"
		if err := outbox.Enqueue(ctx, p.q, subject, events.Event{Data: map[string]any{"kind": kind}}); err != nil {
			return err
		}
	}
	return nil
}

func (p *Publisher) Relay(ctx context.Context, r row) error {
	return p.enqueue(ctx, r.Subject)
}

func (p *Publisher) enqueue(ctx context.Context, subject string) error {
	return outbox.Enqueue(ctx, p.q, subject, events.Event{})
}

func (p *Publisher) orphan(ctx context.Context, subject string) error {
	return outbox.Enqueue(ctx, p.q, subject, events.Event{})
}
`,
}

func writeTree(t *testing.T, sets ...map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{"go.mod": "module example.com/fixture\n\ngo 1.26\n"}
	for _, set := range sets {
		for k, v := range set {
			files[k] = v
		}
	}
	for rel, content := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(rel)), content)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs", "arquitectura"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// siteOf es "ruta:linea" de la primera linea de la fuente que contiene marker.
func siteOf(t *testing.T, sources map[string]string, rel, marker string) string {
	t.Helper()
	for i, line := range strings.Split(sources[rel], "\n") {
		if strings.Contains(line, marker) {
			return rel + ":" + strconv.Itoa(i+1)
		}
	}
	t.Fatalf("%q no aparece en %s", marker, rel)
	return ""
}

func mustAnalyze(t *testing.T, root string) *result {
	t.Helper()
	res, err := analyze(root)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestOutboxPublications(t *testing.T) {
	res := mustAnalyze(t, writeTree(t, alphaFiles))
	if len(res.unresolved) > 0 {
		t.Fatalf("publicaciones sin resolver: %v", res.unresolved)
	}
	want := map[string][]string{
		"alpha.item.created": {"item_id", "note", "tenant_id"},
		"alpha.item.removed": {"email", "tenant_id"},
		"alpha.run.failed":   {"reason", "run_id"},
	}
	if len(res.contracts) != len(want) {
		t.Fatalf("contratos = %v, quiero solo %v", sortedContractKeys(res.contracts), want)
	}
	for subject, fields := range want {
		c, ok := res.contracts[subject]
		if !ok {
			t.Fatalf("falta %s en el registro", subject)
		}
		if c.Opaque || !reflect.DeepEqual(c.Fields, fields) {
			t.Errorf("%s: campos %v (opaco=%v), quiero %v", subject, c.Fields, c.Opaque, fields)
		}
		if !reflect.DeepEqual(c.Publishers, []string{"alpha"}) {
			t.Errorf("%s: publicadores %v", subject, c.Publishers)
		}
	}
	// El sitio que se reporta es la llamada que fija el subject, no el Enqueue del reenviador.
	got := res.repo.rel(res.contracts["alpha.run.failed"].Sites["alpha"][0])
	if site := siteOf(t, alphaFiles, "services/alpha/internal/app/events.go", `"alpha.run.failed"`); got != site {
		t.Errorf("sitio de alpha.run.failed = %s, quiero %s", got, site)
	}
}

func TestConsumerFieldsMatchedAgainstOutboxProducer(t *testing.T) {
	res := mustAnalyze(t, writeTree(t, alphaFiles, betaFiles))
	reads := map[string][]string{}
	for _, c := range res.cons {
		if !c.Opaque {
			reads[c.Subject] = c.Fields
		}
	}
	want := map[string][]string{
		"alpha.item.created": {"item_id", "tenant_id"},
		"alpha.item.removed": {"email", "tenant_id"},
		"alpha.run.failed":   {"run_id", "workflow_id"},
	}
	if !reflect.DeepEqual(reads, want) {
		t.Fatalf("lecturas = %v, quiero %v", reads, want)
	}

	problems := verify(res)
	if len(problems) != 1 {
		t.Fatalf("problemas = %v, quiero solo el de workflow_id", problems)
	}
	p := problems[0]
	for _, part := range []string{
		"alpha.run.failed: beta lee `workflow_id`",
		siteOf(t, betaFiles, "services/beta/internal/adapters/nats/worker.go", `data["workflow_id"]`),
		siteOf(t, alphaFiles, "services/alpha/internal/app/events.go", `"alpha.run.failed"`),
	} {
		if !strings.Contains(p, part) {
			t.Errorf("el problema %q no menciona %q", p, part)
		}
	}
}

func TestUnresolvedSubjectFailsLoudly(t *testing.T) {
	root := writeTree(t, gammaFiles)
	res := mustAnalyze(t, root)
	const file = "services/gamma/internal/adapters/outbox/publisher.go"
	for _, site := range []string{
		siteOf(t, gammaFiles, file, `subject := "gamma." + kind`),
		siteOf(t, gammaFiles, file, `p.enqueue(ctx, r.Subject)`),
		siteOf(t, gammaFiles, file, `func (p *Publisher) orphan`),
	} {
		found := false
		for _, u := range res.unresolved {
			found = found || strings.HasPrefix(u, site+": gamma publica sin subject resoluble")
		}
		if !found {
			t.Errorf("no se reporta %s; reportado: %v", site, res.unresolved)
		}
	}
	if len(res.unresolved) != 3 {
		t.Errorf("reportes = %v, quiero 3", res.unresolved)
	}
	if len(res.contracts) != 0 {
		t.Errorf("un subject sin resolver no puede entrar al registro: %v", sortedContractKeys(res.contracts))
	}

	var stdout, stderr bytes.Buffer
	if code := run(root, false, &stdout, &stderr); code == 0 {
		t.Fatal("la generacion debe fallar con una publicacion sin subject")
	}
	if !strings.Contains(stderr.String(), "::error::"+siteOf(t, gammaFiles, file, `subject := "gamma." + kind`)) {
		t.Errorf("stderr no localiza el fallo: %s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, outputPath)); !os.IsNotExist(err) {
		t.Error("no debe escribirse un registro incompleto")
	}
	if code := run(root, true, &stdout, &stderr); code == 0 {
		t.Error("-check debe fallar con una publicacion sin subject")
	}
}

func TestOneOwnerAcrossPublicationStyles(t *testing.T) {
	res := mustAnalyze(t, writeTree(t, map[string]string{
		"services/delta/bus.go": `package delta

import "example.com/fixture/pkg/events"

func publish(bus *events.Bus) error {
	return bus.Publish("shared.thing.happened", events.Event{Data: map[string]any{"a": 1}})
}
`,
		"services/epsilon/outbox.go": `package epsilon

import (
	"context"

	"example.com/fixture/pkg/events"
	"example.com/fixture/pkg/outbox"
)

func enqueue(ctx context.Context, q outbox.Execer) error {
	return outbox.Enqueue(ctx, q, "shared.thing.happened", events.Event{Data: map[string]any{"b": 2}})
}
`,
	}))
	c := res.contracts["shared.thing.happened"]
	if c == nil || !reflect.DeepEqual(c.Publishers, []string{"delta", "epsilon"}) {
		t.Fatalf("contrato = %+v", c)
	}
	problems := verify(res)
	if len(problems) != 1 || !strings.Contains(problems[0], "shared.thing.happened: lo publican delta (services/delta/bus.go:6) y epsilon (services/epsilon/outbox.go:11)") {
		t.Fatalf("problemas = %v", problems)
	}
}

func TestRegistryAndDrift(t *testing.T) {
	root := writeTree(t, alphaFiles, betaFiles)
	var stdout, stderr bytes.Buffer
	if code := run(root, false, &stdout, &stderr); code != 0 {
		t.Fatalf("generacion = %d: %s", code, stderr.String())
	}
	registry, err := os.ReadFile(filepath.Join(root, registryPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"Resumen: 3 publicaciones, 4 suscripciones, 3 subjects distintos.",
		"| `alpha.item.created` | alpha | beta |",
		"| `alpha.run.failed` | alpha | beta |",
		"- Publica: `alpha.item.created`, `alpha.item.removed`, `alpha.run.failed`",
		"- Consume: `alpha.item.*`, `alpha.item.created`, `alpha.item.removed`, `alpha.run.failed`",
	} {
		if !strings.Contains(string(registry), line+"\n") {
			t.Errorf("EVENTS.md sin %q:\n%s", line, registry)
		}
	}

	// Con los documentos al dia solo queda el campo que falta; un campo nuevo en el
	// payload es drift de los dos registros.
	stderr.Reset()
	if code := run(root, true, &stdout, &stderr); code == 0 || strings.Contains(stderr.String(), "desactualizado") {
		t.Fatalf("check = %d: %s", code, stderr.String())
	}
	publisher := "services/alpha/internal/adapters/outbox/publisher.go"
	writeFile(t, filepath.Join(root, publisher), strings.Replace(alphaFiles[publisher],
		`map[string]any{"email": email}`, `map[string]any{"email": email, "consented_at": "t"}`, 1))
	stderr.Reset()
	if code := run(root, true, &stdout, &stderr); code == 0 {
		t.Fatal("-check debe fallar con un campo nuevo sin regenerar")
	}
	if !strings.Contains(stderr.String(), outputPath+" desactualizado") {
		t.Errorf("no se detecta el drift de %s: %s", outputPath, stderr.String())
	}
	// EVENTS.md no lleva campos: un campo nuevo no lo cambia.
	if strings.Contains(stderr.String(), registryPath+" desactualizado") {
		t.Errorf("drift inesperado de %s: %s", registryPath, stderr.String())
	}
}

func TestMatchSubject(t *testing.T) {
	cases := []struct {
		pattern, subject string
		want             bool
	}{
		{"mail.>", "mail.mailbox.created", true},
		{"mail.>", "mail", false},
		{"mail.mailbox.>", "mail.domain.created", false},
		{"transactional.email.*", "transactional.email.sent", true},
		{"transactional.email.*", "transactional.email.sent.x", false},
		{"a.b.c", "a.b.c", true},
		{"a.*.c", "a.b.d", false},
	}
	for _, c := range cases {
		if got := matchSubject(c.pattern, c.subject); got != c.want {
			t.Errorf("matchSubject(%q, %q) = %v", c.pattern, c.subject, got)
		}
	}
}
