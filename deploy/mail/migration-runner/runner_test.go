package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	sourcePassword = "s3creta-de-origen"
	canaryValue    = "canario-que-no-debe-heredarse"
)

// fakeImapsync escribe un imapsync de prueba: anota argv y entorno de cada invocacion, copia el
// contenido y los permisos de los ficheros de contrasena tal como los ve el proceso y responde con
// una muestra real de testdata segun el modo de la invocacion (rec/mode.<n> o rec/mode).
type fakeImapsync struct {
	bin string
	rec string
}

func newFakeImapsync(t *testing.T) *fakeImapsync {
	t.Helper()
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec")
	if err := os.MkdirAll(rec, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
REC=%q
DATA=%q
n=$(cat "$REC/count" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$REC/count"
printf '%%s\n' "$@" > "$REC/argv.$n"
env > "$REC/env.$n"
pwd > "$REC/pwd.$n"
for a in "$@"; do
  case "$a" in
    --passfile1=*) f="${a#--passfile1=}"; cat "$f" > "$REC/pass1.$n"; stat -c %%a "$f" > "$REC/mode1.$n"; stat -c %%a "$(dirname "$f")" > "$REC/dirmode.$n"; echo "$f" > "$REC/passpath.$n" ;;
    --passfile2=*) f="${a#--passfile2=}"; cat "$f" > "$REC/pass2.$n"; stat -c %%a "$f" > "$REC/mode2.$n" ;;
  esac
done
mode=$(cat "$REC/mode.$n" 2>/dev/null || cat "$REC/mode" 2>/dev/null || echo "imapsync-ok.txt 0")
set -- $mode
if [ "$1" = "SLEEP" ]; then
  sleep 60 &
  echo $! > "$REC/child.pid"
  echo $$ > "$REC/main.pid"
  wait
  exit 0
fi
cat "$DATA/$1"
exit "$2"
`, rec, data)
	bin := filepath.Join(dir, "imapsync")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &fakeImapsync{bin: bin, rec: rec}
}

func (f *fakeImapsync) setMode(t *testing.T, name, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.rec, name), []byte(mode), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeImapsync) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.rec, name))
	if err != nil {
		t.Fatalf("no se registro %s: %v", name, err)
	}
	return string(b)
}

func (f *fakeImapsync) invocations() int {
	b, err := os.ReadFile(filepath.Join(f.rec, "count"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

func processAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z"
}

// safeBuffer recoge los registros del ejecutor para comprobar que no filtran secretos.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeService es mail-migration en su API de ejecutor.
type fakeService struct {
	mu        sync.Mutex
	claimJobs []*ClaimedJob
	beats     []map[string]any
	completes []map[string]any
	beatReply func(n int) (int, string)
	doneReply func(n int) int
	srv       *httptest.Server
}

func newFakeService(t *testing.T) *fakeService {
	t.Helper()
	s := &fakeService{
		beatReply: func(int) (int, string) { return 200, `{"data":{"cancel":false,"lease_seconds":1}}` },
		doneReply: func(int) int { return 200 },
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/claim":
			if len(s.claimJobs) == 0 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			job := s.claimJobs[0]
			s.claimJobs = s.claimJobs[1:]
			out, _ := json.Marshal(map[string]any{"data": job})
			_, _ = w.Write(out)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			s.beats = append(s.beats, body)
			status, reply := s.beatReply(len(s.beats))
			w.WriteHeader(status)
			_, _ = w.Write([]byte(reply))
		case strings.HasSuffix(r.URL.Path, "/complete"):
			s.completes = append(s.completes, body)
			status := s.doneReply(len(s.completes))
			w.WriteHeader(status)
			if status >= 400 {
				code := "INTERNAL"
				if status == http.StatusConflict {
					code = "LEASE_LOST"
				}
				_, _ = w.Write([]byte(`{"error":{"code":"` + code + `"}}`))
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *fakeService) api() *API {
	base, _ := url.Parse(s.srv.URL)
	return NewAPI(base, testKey, "runner-prueba")
}

func (s *fakeService) completed() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.completes...)
}

func (s *fakeService) phases() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, b := range s.beats {
		out = append(out, b["phase"].(string))
	}
	return out
}

func testJob(host string) *ClaimedJob {
	j := &ClaimedJob{JobID: testJobID, TenantID: testTenantID, LeaseID: testLeaseID, Attempt: 1, LeaseSeconds: 1}
	j.Source = Source{Host: host, Port: 993, TLS: "ssl", Username: "ana@origen.example", Password: sourcePassword}
	j.Destination.Username = "ana@empresa.example"
	return j
}

type harness struct {
	fake    *fakeImapsync
	svc     *fakeService
	runner  *Runner
	logs    *safeBuffer
	workDir string
}

func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	fake := newFakeImapsync(t)
	svc := newFakeService(t)
	logs := &safeBuffer{}
	cfg := Config{
		Environment: "production", RunnerKey: testKey, RunnerID: "runner-prueba",
		DestHost: "dovecot", DestPort: 993, DestTLSServerName: "mail.example.test",
		MasterUser: "migracion", MasterPass: testMaster,
		ClamdAddr: "clamav:3310", ScanMaxBytes: defaultScanMax, ScanTimeout: defaultScanLimit,
		JobTimeout: time.Minute, PollInterval: 50 * time.Millisecond,
		ImapsyncBin: fake.bin, WorkDir: filepath.Join(t.TempDir(), "run"), Self: "/usr/local/bin/migration-runner",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	guard := NewSourceGuard(fakeResolver{"imap.origen.example": {"93.184.216.34"}, "interno.example": {"10.0.0.5"}}, cfg.AllowPrivateSources)
	runner := NewRunner(cfg, svc.api(), guard, slog.New(slog.NewJSONHandler(logs, nil)))
	runner.completeBase = 10 * time.Millisecond
	runner.completeBudget = 5 * time.Second
	return &harness{fake: fake, svc: svc, runner: runner, logs: logs, workDir: cfg.WorkDir}
}

func (h *harness) jobDirs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(h.workDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestEjecutorDosPasadasSinSecretosEnArgvEntornoNiRegistros(t *testing.T) {
	t.Setenv("MAIL_MIGRATION_RUNNER_KEY", testKey)
	t.Setenv("DOVECOT_MIGRATION_MASTER_PASS", testMaster)
	t.Setenv("CANARIO", canaryValue)
	h := newHarness(t, nil)
	h.fake.setMode(t, "mode.2", "imapsync-ok-repaso.txt 0")

	h.runner.Execute(context.Background(), testJob("imap.origen.example"))

	if got := h.fake.invocations(); got != 2 {
		t.Fatalf("invocaciones %d, quiero 2 (initial y catchup)", got)
	}
	done := h.svc.completed()
	if len(done) != 1 || done[0]["outcome"] != "succeeded" || done[0]["error"] != nil || done[0]["lease_id"] != testLeaseID {
		t.Fatalf("cierre: %+v", done)
	}
	progress := done[0]["progress"].(map[string]any)
	if progress["messages_copied"].(float64) != 6 || progress["messages_skipped"].(float64) != 6 || progress["bytes_copied"].(float64) != 432927 {
		t.Fatalf("progreso acumulado de las dos pasadas: %v", progress)
	}
	if phases := h.svc.phases(); len(phases) < 2 || phases[0] != "initial" || phases[len(phases)-1] != "catchup" {
		t.Fatalf("fases informadas: %v", phases)
	}

	for n := 1; n <= 2; n++ {
		argv := h.fake.read(t, fmt.Sprintf("argv.%d", n))
		env := h.fake.read(t, fmt.Sprintf("env.%d", n))
		for _, secret := range []string{sourcePassword, testMaster, testKey, canaryValue} {
			if strings.Contains(argv, secret) {
				t.Fatalf("pasada %d: %q esta en argv", n, secret)
			}
			if strings.Contains(env, secret) {
				t.Fatalf("pasada %d: %q esta en el entorno del hijo", n, secret)
			}
		}
		if got := h.fake.read(t, fmt.Sprintf("pass1.%d", n)); got != sourcePassword {
			t.Fatalf("pasada %d: el fichero de origen tiene %q", n, got)
		}
		if got := h.fake.read(t, fmt.Sprintf("pass2.%d", n)); got != testMaster {
			t.Fatalf("pasada %d: el fichero de destino tiene %q", n, got)
		}
		for _, name := range []string{"mode1", "mode2"} {
			if got := strings.TrimSpace(h.fake.read(t, fmt.Sprintf("%s.%d", name, n))); got != "400" {
				t.Fatalf("pasada %d: %s con permisos %s, quiero 400", n, name, got)
			}
		}
		if got := strings.TrimSpace(h.fake.read(t, fmt.Sprintf("dirmode.%d", n))); got != "700" {
			t.Fatalf("pasada %d: el directorio del trabajo tiene permisos %s, quiero 700", n, got)
		}
		keys := map[string]bool{}
		for _, line := range strings.Split(strings.TrimSpace(env), "\n") {
			keys[strings.SplitN(line, "=", 2)[0]] = true
		}
		allowed := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true, "PWD": true, "SHLVL": true, "_": true, "OLDPWD": true,
			"MIGRATION_CLAMD_ADDR": true, "MIGRATION_SCAN_MAX_BYTES": true, "MIGRATION_SCAN_TIMEOUT": true}
		for k := range keys {
			if !allowed[k] {
				t.Fatalf("pasada %d: el hijo heredo la variable %s", n, k)
			}
		}
	}

	if dirs := h.jobDirs(t); len(dirs) != 0 {
		t.Fatalf("el directorio del trabajo debe borrarse: %v", dirs)
	}
	logs := h.logs.String()
	for _, secret := range []string{sourcePassword, testMaster, testKey, "ana@origen.example", "imap.origen.example"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("los registros filtran %q:\n%s", secret, logs)
		}
	}
}

func TestEjecutorLineaDeOrdenes(t *testing.T) {
	h := newHarness(t, nil)
	h.runner.Execute(context.Background(), testJob("imap.origen.example"))
	args := strings.Split(strings.TrimSpace(h.fake.read(t, "argv.1")), "\n")
	has := func(want string) bool {
		for _, a := range args {
			if a == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"--host1=93.184.216.34", "--port1=993", "--user1=ana@origen.example", "--ssl1", "--notls1", "--nosslcheck",
		"--sslargs1=SSL_verify_mode=1", "--sslargs1=SSL_verifycn_name=imap.origen.example", "--sslargs1=SSL_hostname=imap.origen.example",
		"--host2=dovecot", "--port2=993", "--user2=ana@empresa.example*migracion@platform.local", "--ssl2",
		"--sslargs2=SSL_verify_mode=1", "--sslargs2=SSL_verifycn_name=mail.example.test", "--sslargs2=SSL_hostname=mail.example.test",
		"--nolog", "--noreleasecheck", "--noid", "--pipemess=/usr/local/bin/migration-runner scan-filter",
	} {
		if !has(want) {
			t.Errorf("falta %q en la linea de ordenes:\n%s", want, strings.Join(args, "\n"))
		}
	}
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--delete"), strings.HasPrefix(a, "--expunge"), strings.HasPrefix(a, "--nodry"), a == "--host1=imap.origen.example":
			t.Errorf("argumento peligroso: %s", a)
		}
	}
	if pwd := strings.TrimSpace(h.fake.read(t, "pwd.1")); !strings.HasPrefix(pwd, h.workDir) {
		t.Errorf("imapsync corre en %s, fuera del directorio efimero %s", pwd, h.workDir)
	}
}

func TestEjecutorOrigenBloqueadoNoEjecutaImapsync(t *testing.T) {
	for _, host := range []string{"interno.example", "localhost", "10.0.0.5", "127.0.0.1", "169.254.169.254", "::1", "::ffff:10.0.0.1", "fd00:ec2::254", "0x7f000001"} {
		t.Run(host, func(t *testing.T) {
			h := newHarness(t, nil)
			h.runner.Execute(context.Background(), testJob(host))
			if got := h.fake.invocations(); got != 0 {
				t.Fatalf("imapsync se ejecuto %d veces contra %s", got, host)
			}
			done := h.svc.completed()
			if len(done) != 1 || done[0]["outcome"] != "failed" {
				t.Fatalf("cierre: %+v", done)
			}
			code := done[0]["error"].(map[string]any)["code"]
			want := codeSourceBlockedAddress
			if host == "0x7f000001" {
				want = codeSourceUnreachable
			}
			if code != want {
				t.Fatalf("codigo %v, quiero %s", code, want)
			}
			if dirs := h.jobDirs(t); len(dirs) != 0 {
				t.Fatalf("no debe crearse directorio de trabajo: %v", dirs)
			}
		})
	}
}

func TestEjecutorModoPruebaAdmiteOrigenPrivado(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Environment = "test"; c.AllowPrivateSources = true })
	h.runner.Execute(context.Background(), testJob("interno.example"))
	if got := h.fake.invocations(); got != 2 {
		t.Fatalf("invocaciones %d", got)
	}
	if !strings.Contains(h.fake.read(t, "argv.1"), "--host1=10.0.0.5") {
		t.Fatal("debe conectar a la IP resuelta")
	}
}

func TestEjecutorClasificaCadaResultado(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		outcome     string
		code        string
		invocations int
	}{
		{"autenticacion de origen", "imapsync-auth.txt 161", "failed", codeSourceAuthFailed, 1},
		{"origen inaccesible", "imapsync-inaccesible.txt 101", "failed", codeSourceUnreachable, 1},
		{"certificado de origen", "imapsync-tls-nombre.txt 101", "failed", codeSourceTLSFailed, 1},
		{"cuota", "imapsync-cuota.txt 113", "failed", codeQuotaExceeded, 1},
		{"virus con salida 0", "imapsync-virus.txt 0", "failed", codeVirusFound, 2},
		{"salida desconocida", "imapsync-ok.txt 77", "failed", codeImapsyncFailed, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.fake.setMode(t, "mode", c.mode)
			h.runner.Execute(context.Background(), testJob("imap.origen.example"))
			done := h.svc.completed()
			if len(done) != 1 || done[0]["outcome"] != c.outcome {
				t.Fatalf("cierre: %+v", done)
			}
			e := done[0]["error"].(map[string]any)
			if e["code"] != c.code {
				t.Fatalf("codigo %v, quiero %s", e["code"], c.code)
			}
			msg := e["message"].(string)
			if len(msg) == 0 || len(msg) > 300 || strings.ContainsAny(msg, "\n\x00") {
				t.Fatalf("mensaje %q", msg)
			}
			if got := h.fake.invocations(); got != c.invocations {
				t.Fatalf("invocaciones %d, quiero %d", got, c.invocations)
			}
		})
	}
}

func TestEjecutorCancelacionMataElGrupoDeProcesos(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.setMode(t, "mode", "SLEEP")
	h.svc.beatReply = func(n int) (int, string) {
		return 200, fmt.Sprintf(`{"data":{"cancel":%v,"lease_seconds":1}}`, n >= 2)
	}
	start := time.Now()
	h.runner.Execute(context.Background(), testJob("imap.origen.example"))
	if time.Since(start) > 15*time.Second {
		t.Fatalf("tardo %s", time.Since(start))
	}
	done := h.svc.completed()
	if len(done) != 1 || done[0]["outcome"] != "cancelled" || done[0]["error"] != nil {
		t.Fatalf("cierre: %+v", done)
	}
	if got := h.fake.invocations(); got != 1 {
		t.Fatalf("una cancelacion no debe lanzar la segunda pasada: %d", got)
	}
	assertDead(t, h.fake, "main.pid", "child.pid")
	if dirs := h.jobDirs(t); len(dirs) != 0 {
		t.Fatalf("quedan ficheros del trabajo: %v", dirs)
	}
}

func assertDead(t *testing.T, f *fakeImapsync, names ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, name := range names {
		pid, err := strconv.Atoi(strings.TrimSpace(f.read(t, name)))
		if err != nil {
			t.Fatal(err)
		}
		for processAlive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("el proceso %s (%d) sigue vivo", name, pid)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestEjecutorArrendamientoPerdidoAbandonaSinCerrar(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.setMode(t, "mode", "SLEEP")
	h.svc.beatReply = func(n int) (int, string) {
		if n >= 2 {
			return http.StatusConflict, `{"error":{"code":"LEASE_LOST","message":"otro ejecutor"}}`
		}
		return 200, `{"data":{"cancel":false,"lease_seconds":1}}`
	}
	h.runner.Execute(context.Background(), testJob("imap.origen.example"))
	if done := h.svc.completed(); len(done) != 0 {
		t.Fatalf("con el arrendamiento perdido no se cierra: %+v", done)
	}
	assertDead(t, h.fake, "main.pid", "child.pid")
	if dirs := h.jobDirs(t); len(dirs) != 0 {
		t.Fatalf("quedan ficheros del trabajo: %v", dirs)
	}
}

func TestEjecutorSinLatidosAceptadosDuranteElArrendamientoAbandona(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.setMode(t, "mode", "SLEEP")
	h.svc.beatReply = func(n int) (int, string) {
		if n >= 2 {
			return 500, `{"error":{"code":"INTERNAL"}}`
		}
		return 200, `{"data":{"cancel":false,"lease_seconds":1}}`
	}
	h.runner.Execute(context.Background(), testJob("imap.origen.example"))
	if done := h.svc.completed(); len(done) != 0 {
		t.Fatalf("no debe cerrar tras perder el arrendamiento: %+v", done)
	}
	assertDead(t, h.fake, "main.pid", "child.pid")
}

func TestEjecutorPlazoPorPasada(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.JobTimeout = 700 * time.Millisecond })
	h.fake.setMode(t, "mode", "SLEEP")
	h.runner.Execute(context.Background(), testJob("imap.origen.example"))
	done := h.svc.completed()
	if len(done) != 1 || done[0]["outcome"] != "failed" || done[0]["error"].(map[string]any)["code"] != codeTimeout {
		t.Fatalf("cierre: %+v", done)
	}
	assertDead(t, h.fake, "main.pid", "child.pid")
	if dirs := h.jobDirs(t); len(dirs) != 0 {
		t.Fatalf("quedan ficheros del trabajo: %v", dirs)
	}
}

func TestEjecutorApagadoAbandonaSinCerrar(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.setMode(t, "mode", "SLEEP")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for h.fake.invocations() == 0 {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	h.runner.Execute(ctx, testJob("imap.origen.example"))
	if done := h.svc.completed(); len(done) != 0 {
		t.Fatalf("al apagarse no se cierra el trabajo: %+v", done)
	}
	assertDead(t, h.fake, "main.pid", "child.pid")
	if dirs := h.jobDirs(t); len(dirs) != 0 {
		t.Fatalf("quedan ficheros del trabajo: %v", dirs)
	}
}

func TestEjecutorCierreConReintentosAcotados(t *testing.T) {
	h := newHarness(t, nil)
	h.svc.doneReply = func(n int) int {
		if n < 3 {
			return http.StatusServiceUnavailable
		}
		return 200
	}
	h.runner.Execute(context.Background(), testJob("imap.origen.example"))
	if got := len(h.svc.completed()); got != 3 {
		t.Fatalf("intentos de cierre %d, quiero 3", got)
	}

	h = newHarness(t, nil)
	h.runner.completeBudget = 300 * time.Millisecond
	h.svc.doneReply = func(int) int { return http.StatusServiceUnavailable }
	start := time.Now()
	h.runner.Execute(context.Background(), testJob("imap.origen.example"))
	if got := len(h.svc.completed()); got < 2 || time.Since(start) > 5*time.Second {
		t.Fatalf("intentos %d en %s: debe reintentar y rendirse dentro del presupuesto", got, time.Since(start))
	}
}

func TestEjecutorCierreNoReintentaLoDefinitivo(t *testing.T) {
	for name, status := range map[string]int{"arrendamiento perdido": http.StatusConflict, "rechazo": http.StatusUnprocessableEntity} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.svc.doneReply = func(int) int { return status }
			h.runner.Execute(context.Background(), testJob("imap.origen.example"))
			if got := len(h.svc.completed()); got != 1 {
				t.Fatalf("intentos %d, quiero 1", got)
			}
		})
	}
}

func TestEjecutorTrabajoInvalidoSeCierraSinEjecutar(t *testing.T) {
	h := newHarness(t, nil)
	job := testJob("imap.origen.example")
	job.Destination.Username = "otro@empresa.example*maestro"
	h.runner.Execute(context.Background(), job)
	done := h.svc.completed()
	if len(done) != 1 || done[0]["outcome"] != "failed" || h.fake.invocations() != 0 {
		t.Fatalf("cierre %+v, invocaciones %d", done, h.fake.invocations())
	}
}

func TestEjecutorLoopReclamaYCierraHastaElApagado(t *testing.T) {
	h := newHarness(t, nil)
	h.svc.claimJobs = []*ClaimedJob{testJob("imap.origen.example")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.runner.Loop(ctx); close(done) }()
	deadline := time.Now().Add(15 * time.Second)
	for len(h.svc.completed()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("el bucle no termino al apagarse")
	}
	if got := len(h.svc.completed()); got != 1 {
		t.Fatalf("cierres %d", got)
	}
}

func TestBuildArgsModosTLSYFormas(t *testing.T) {
	dir := t.TempDir()
	secrets := &secretFiles{dir: dir, pass1: dir + "/source.pass", pass2: dir + "/dest.pass"}
	cfg := Config{DestHost: "dovecot", DestPort: 993, DestTLSServerName: "mail.example.test", MasterUser: "migracion", Self: "/usr/local/bin/migration-runner", ClamdAddr: "clamav:3310"}
	target := SourceTarget{IP: mustAddr("93.184.216.34"), VerifyName: "imap.origen.example"}
	job := testJob("imap.origen.example")

	job.Source.TLS = "starttls"
	job.Source.Port = 143
	args := strings.Join(buildArgs(cfg, passSpec{job: job, target: target, secrets: secrets}), "\n")
	for _, want := range []string{"--tls1", "--nossl1", "--sslargs1=SSL_verify_mode=1", "--port1=143"} {
		if !strings.Contains(args, want) {
			t.Errorf("starttls: falta %s", want)
		}
	}

	job.Source.TLS = "none"
	args = strings.Join(buildArgs(cfg, passSpec{job: job, target: target, secrets: secrets}), "\n")
	if strings.Contains(args, "--sslargs1") || !strings.Contains(args, "--nossl1") || !strings.Contains(args, "--notls1") || strings.Contains(args, "--ssl1") || strings.Contains(args, "--tls1") {
		t.Errorf("modo none:\n%s", args)
	}
	if !strings.Contains(args, "--sslargs2=SSL_verify_mode=1") {
		t.Error("el destino se verifica siempre")
	}

	literal := SourceTarget{IP: mustAddr("2606:4700:4700::1111"), VerifyName: "2606:4700:4700::1111", IsLiteral: true}
	job.Source.TLS = "ssl"
	args = strings.Join(buildArgs(cfg, passSpec{job: job, target: literal, secrets: secrets}), "\n")
	if strings.Contains(args, "--sslargs1=SSL_hostname") || !strings.Contains(args, "--sslargs1=SSL_verifycn_name=2606:4700:4700::1111") {
		t.Errorf("IP literal: sin SNI y verificando contra la IP:\n%s", args)
	}

	cfg.ClamdAddr = ""
	args = strings.Join(buildArgs(cfg, passSpec{job: job, target: target, secrets: secrets}), "\n")
	if strings.Contains(args, "--pipemess") {
		t.Error("sin clamd no hay filtro")
	}

	job.Source.Username = "--delete1"
	for _, a := range buildArgs(cfg, passSpec{job: job, target: target, secrets: secrets}) {
		if a == "--delete1" {
			t.Fatal("un valor del usuario no puede convertirse en una opcion")
		}
	}
}

func TestSecretosSeCreanPrivadosYSeBorran(t *testing.T) {
	root := filepath.Join(t.TempDir(), "run")
	s, err := newSecretFiles(root, "origen", "destino")
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{s.pass1: "origen", s.pass2: "destino"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o400 {
			t.Fatalf("%s: %v %v", path, info, err)
		}
		if b, _ := os.ReadFile(path); string(b) != want {
			t.Fatalf("contenido de %s", path)
		}
	}
	if info, _ := os.Stat(s.dir); info.Mode().Perm() != 0o700 || !strings.HasPrefix(filepath.Base(s.dir), "job-") {
		t.Fatalf("directorio %s con permisos %v", s.dir, info.Mode())
	}
	other, _ := newSecretFiles(root, "a", "b")
	if other.dir == s.dir {
		t.Fatal("el nombre debe ser aleatorio")
	}
	s.remove()
	other.remove()
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("quedan %d entradas", len(entries))
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
