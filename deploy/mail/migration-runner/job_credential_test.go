package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

const jobCredential = "cfmj1.6b1d8f4e-0f0b-4d55-a2a0-1c7d2f6d3a11.0c2c6a0e-8a53-4f0e-9a3c-5d1f4b7a9e22.q3Zx9pQm2T-vN8r_KaLw0eY4uHs6BdG1cJfXo5tRiA7"

func TestConfigSinMaestroCompartidoEsActiva(t *testing.T) {
	env := baseEnv()
	delete(env, "DOVECOT_MIGRATION_MASTER_USER")
	delete(env, "DOVECOT_MIGRATION_MASTER_PASS")
	cfg, state, err := load(env)
	if state != StateActive || err != nil {
		t.Fatalf("sin maestro compartido debe estar activa: estado %v, error %v", state, err)
	}
	if cfg.SharedMaster() {
		t.Fatal("no debe tener maestro compartido")
	}
	job := &ClaimedJob{}
	job.Destination.Username = "ana@empresa.example"
	if _, ok := cfg.destinationSecret(job); ok {
		t.Fatal("sin maestro y sin credencial de trabajo no hay con que entrar al buzon")
	}
}

func TestConfigMaestroCompartidoIncompletoEsInvalido(t *testing.T) {
	for name, drop := range map[string]string{"sin contrasena": "DOVECOT_MIGRATION_MASTER_PASS", "sin usuario": "DOVECOT_MIGRATION_MASTER_USER"} {
		env := baseEnv()
		delete(env, drop)
		if _, state, err := load(env); state != StateMisconfigured || err == nil {
			t.Errorf("%s: estado %v, error %v", name, state, err)
		}
	}
}

func TestLaCredencialDeTrabajoTieneSuPropioUsuarioYSecreto(t *testing.T) {
	cfg, _, _ := load(baseEnv())
	job := &ClaimedJob{}
	job.Destination.Username = "ana@empresa.example"

	if got := cfg.destinationUser(job); got != "ana@empresa.example*migracion@platform.local" {
		t.Fatalf("usuario con el maestro compartido: %q", got)
	}
	if pass, ok := cfg.destinationSecret(job); !ok || pass != testMaster {
		t.Fatalf("secreto con el maestro compartido: %q %v", pass, ok)
	}

	job.Destination.Password = jobCredential
	if got := cfg.destinationUser(job); got != "ana@empresa.example" {
		t.Fatalf("con credencial de trabajo el usuario es el buzon a secas: %q", got)
	}
	if pass, ok := cfg.destinationSecret(job); !ok || pass != jobCredential {
		t.Fatalf("con credencial de trabajo no debe usarse el maestro: %q", pass)
	}
}

func TestEjecutorConCredencialDeTrabajoEntraSoloAlBuzonSinMaestro(t *testing.T) {
	t.Setenv("CANARIO", canaryValue)
	h := newHarness(t, func(c *Config) { c.MasterUser, c.MasterPass = "", "" })
	h.fake.setMode(t, "mode.2", "imapsync-ok-repaso.txt 0")
	job := testJob("imap.origen.example")
	job.Destination.Password = jobCredential

	h.runner.Execute(context.Background(), job)

	done := h.svc.completed()
	if len(done) != 1 || done[0]["outcome"] != "succeeded" {
		t.Fatalf("cierre: %+v", done)
	}
	for n := 1; n <= 2; n++ {
		argv := h.fake.read(t, fmt.Sprintf("argv.%d", n))
		if !strings.Contains(argv, "--user2=ana@empresa.example\n") || strings.Contains(argv, "@platform.local") {
			t.Fatalf("pasada %d: el destino debe ser el buzon a secas:\n%s", n, argv)
		}
		for _, secret := range []string{jobCredential, sourcePassword, testKey, canaryValue} {
			if strings.Contains(argv, secret) || strings.Contains(h.fake.read(t, fmt.Sprintf("env.%d", n)), secret) {
				t.Fatalf("pasada %d: %q esta en argv o en el entorno del hijo", n, secret)
			}
		}
		if got := h.fake.read(t, fmt.Sprintf("pass2.%d", n)); got != jobCredential {
			t.Fatalf("pasada %d: el fichero de destino tiene %q", n, got)
		}
		if got := strings.TrimSpace(h.fake.read(t, fmt.Sprintf("mode2.%d", n))); got != "400" {
			t.Fatalf("pasada %d: fichero de destino con permisos %s", n, got)
		}
	}
	if dirs := h.jobDirs(t); len(dirs) != 0 {
		t.Fatalf("el directorio del trabajo debe borrarse: %v", dirs)
	}
	if logs := h.logs.String(); strings.Contains(logs, jobCredential) || strings.Contains(logs, "q3Zx9pQm") {
		t.Fatalf("los registros filtran la credencial de destino:\n%s", logs)
	}
}

func TestEjecutorPrefiereLaCredencialDeTrabajoAlMaestroCompartido(t *testing.T) {
	h := newHarness(t, nil)
	job := testJob("imap.origen.example")
	job.Destination.Password = jobCredential
	h.runner.Execute(context.Background(), job)

	if got := h.fake.read(t, "pass2.1"); got != jobCredential {
		t.Fatalf("con las dos disponibles debe usarse la del trabajo: %q", got)
	}
	if argv := h.fake.read(t, "argv.1"); strings.Contains(argv, "@platform.local") {
		t.Fatalf("no debe pasar por el maestro:\n%s", argv)
	}
}

func TestEjecutorSinCredencialNiMaestroFallaSinLanzarImapsync(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MasterUser, c.MasterPass = "", "" })
	h.runner.Execute(context.Background(), testJob("imap.origen.example"))

	if got := h.fake.invocations(); got != 0 {
		t.Fatalf("no debe lanzarse imapsync sin credencial de destino: %d invocaciones", got)
	}
	done := h.svc.completed()
	if len(done) != 1 || done[0]["outcome"] != "failed" {
		t.Fatalf("cierre: %+v", done)
	}
	if e, _ := done[0]["error"].(map[string]any); e["code"] != codeDestinationFailed {
		t.Fatalf("error: %v", done[0]["error"])
	}
	if dirs := h.jobDirs(t); len(dirs) != 0 {
		t.Fatalf("no debe quedar directorio del trabajo: %v", dirs)
	}
}

func TestUnaCredencialDeDestinoMalFormadaSeRechazaAntesDeLaLineaDeOrdenes(t *testing.T) {
	for name, bad := range map[string]string{
		"sin prefijo":     "abc.6b1d8f4e-0f0b-4d55-a2a0-1c7d2f6d3a11.0c2c6a0e-8a53-4f0e-9a3c-5d1f4b7a9e22.q3Zx",
		"con espacio":     "cfmj1.6b1d8f4e-0f0b-4d55-a2a0-1c7d2f6d3a11 0c2c6a0e-8a53-4f0e-9a3c-5d1f4b7a9e22.q3Zx9pQm",
		"con salto":       jobCredential + "\nextra",
		"con asterisco":   "cfmj1.6b1d8f4e-0f0b-4d55-a2a0-1c7d2f6d3a11.0c2c6a0e-8a53-4f0e-9a3c*5d1f4b7a9e22.q3Zx",
		"demasiado corta": "cfmj1.a",
		"demasiado larga": "cfmj1." + strings.Repeat("a", 300),
	} {
		job := testJob("imap.origen.example")
		job.Destination.Password = bad
		if err := job.validate(); err == nil {
			t.Errorf("%s: se acepto la credencial %q", name, bad)
		}
	}
	job := testJob("imap.origen.example")
	job.Destination.Password = jobCredential
	if err := job.validate(); err != nil {
		t.Fatalf("la credencial que emite el servicio debe ser valida: %v", err)
	}
}
