package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Un origen hostil que comprometiera a imapsync corre con el mismo usuario que el ejecutor: si la
// memoria del ejecutor fuera legible por /proc, se llevaria la clave del servicio y la contrasena
// del maestro de Dovecot, que estan en su entorno.
func TestProtegerProcesoCierraProcDelEjecutorAUnHijoDelMismoUsuario(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root lee /proc de cualquiera")
	}
	environ := fmt.Sprintf("/proc/%d/environ", os.Getpid())
	if out, err := exec.Command("cat", environ).Output(); err != nil || len(out) == 0 {
		t.Fatalf("premisa: un hijo del mismo usuario debe poder leer %s antes de proteger: %v", environ, err)
	}
	if err := protectProcess(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cat", environ).Output(); err == nil {
		t.Fatalf("un hijo del mismo usuario leyo el entorno del ejecutor (%d bytes)", len(out))
	}
}

func TestGuardaSSRFBloqueaTodoElBloqueDeProtocolosDeIETF(t *testing.T) {
	g := NewSourceGuard(fakeResolver{}, false)
	for _, host := range []string{"2001:1::1", "2001:1::2", "2001:3::1", "2001:4:112::1", "2001:100::1", "2001:1ff::1"} {
		if _, err := g.Resolve(context.Background(), host); !errors.Is(err, errBlockedAddress) {
			t.Errorf("%s pertenece a 2001::/23 (asignaciones de protocolos) y debe bloquearse, error: %v", host, err)
		}
	}
	for _, host := range []string{"2001:200::1", "2001:4860:4860::8888"} {
		if _, err := g.Resolve(context.Background(), host); err != nil {
			t.Errorf("%s es publica y debe permitirse: %v", host, err)
		}
	}
}

// El servicio ya limita los puertos, pero el ejecutor es el que sale a Internet: si el servicio o su
// base fueran manipulados, el ejecutor no puede convertirse en un sondeo de puertos arbitrarios.
func TestEjecutorRechazaPuertoDeOrigenFueraDeLaLista(t *testing.T) {
	for _, port := range []int{25, 22, 80, 443, 3306, 5432, 6379, 8057, 587, 465, 994, 144} {
		t.Run(fmt.Sprint(port), func(t *testing.T) {
			h := newHarness(t, nil)
			job := testJob("imap.origen.example")
			job.Source.Port = port
			h.runner.Execute(context.Background(), job)
			if got := h.fake.invocations(); got != 0 {
				t.Fatalf("imapsync se ejecuto %d veces contra el puerto %d", got, port)
			}
			done := h.svc.completed()
			if len(done) != 1 || done[0]["outcome"] != "failed" {
				t.Fatalf("cierre: %+v", done)
			}
			if code := done[0]["error"].(map[string]any)["code"]; code != codeSourceBlockedAddress {
				t.Fatalf("codigo %v, quiero %s", code, codeSourceBlockedAddress)
			}
			if dirs := h.jobDirs(t); len(dirs) != 0 {
				t.Fatalf("no debe crearse directorio de trabajo: %v", dirs)
			}
		})
	}
}

func TestEjecutorAdmiteLosPuertosConfigurados(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.SourcePorts = []int{1143} })
	job := testJob("imap.origen.example")
	job.Source.Port = 1143
	h.runner.Execute(context.Background(), job)
	if got := h.fake.invocations(); got != 2 {
		t.Fatalf("invocaciones %d, quiero 2", got)
	}
}

func TestConfigPuertosDeOrigen(t *testing.T) {
	cfg, state, err := load(baseEnv())
	if state != StateActive || err != nil || len(cfg.SourcePorts) != 2 || cfg.SourcePorts[0] != 143 || cfg.SourcePorts[1] != 993 {
		t.Fatalf("por defecto 143 y 993: %+v %v %v", cfg.SourcePorts, state, err)
	}
	env := baseEnv()
	env["MIGRATION_SOURCE_PORTS"] = "143, 993,1143"
	cfg, state, err = load(env)
	if state != StateActive || err != nil || len(cfg.SourcePorts) != 3 || cfg.SourcePorts[2] != 1143 {
		t.Fatalf("lista configurada: %+v %v %v", cfg.SourcePorts, state, err)
	}
	for _, bad := range []string{"0", "70000", "abc", "143,,993", "143;25"} {
		env := baseEnv()
		env["MIGRATION_SOURCE_PORTS"] = bad
		if _, state, err := load(env); state != StateMisconfigured || err == nil || !strings.Contains(err.Error(), "MIGRATION_SOURCE_PORTS") {
			t.Errorf("%q debe ser una configuracion invalida: %v %v", bad, state, err)
		}
	}
}

// Un origen con millones de carpetas no puede hacer crecer la memoria del ejecutor sin limite: solo
// se guardan las que se informan y el resto se cuenta.
func TestParseAcotaLaMemoriaConMuchasCarpetas(t *testing.T) {
	const total = 20000
	p := newOutputParser()
	for i := 1; i <= total; i++ {
		fmt.Fprintf(p, "Folder %d/%d [Carpeta%d]                       -> [Carpeta%d]\n", i, total, i, i)
		fmt.Fprintf(p, "Host1: folder [Carpeta%d] selected 3 messages, 3.0 KiB\n", i)
		fmt.Fprintf(p, "msg Carpeta%d/1 {1000}  copied to Carpeta%d/1 0.5 msgs/s\n", i, i)
	}
	fmt.Fprintln(p, "++++ End looping on each folder")
	if len(p.folders) > maxFolders {
		t.Fatalf("el analizador guarda %d carpetas, tope %d", len(p.folders), maxFolders)
	}
	pr := p.Progress()
	if pr.FoldersTotal != total || pr.FoldersDone != total {
		t.Fatalf("carpetas %d/%d, quiero %d/%d", pr.FoldersDone, pr.FoldersTotal, total, total)
	}
	if pr.MessagesCopied != total || pr.MessagesSkipped != 2*total || pr.BytesCopied != 1000*total {
		t.Fatalf("copiados=%d omitidos=%d bytes=%d", pr.MessagesCopied, pr.MessagesSkipped, pr.BytesCopied)
	}
	if len(pr.Folders) != maxFolders {
		t.Fatalf("se informan %d carpetas, quiero %d", len(pr.Folders), maxFolders)
	}
}
