package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

const (
	maxScanUnavailable = 5
	errorsMax          = 500
	waitDelay          = 10 * time.Second
	childPath          = "/usr/local/bin:/usr/bin:/bin"
	sourceSSLVersion   = "SSLv23:!SSLv2:!SSLv3:!TLSv1:!TLSv1_1"
)

var (
	errCancelRequested = errors.New("cancelacion pedida por el servicio")
	errLeaseLost       = errors.New("arrendamiento perdido")
)

// secretFiles son los ficheros con las contrasenas de una pasada, dentro del directorio efimero
// del trabajo. imapsync los lee con --passfile: la contrasena no pasa por argv ni por el entorno.
type secretFiles struct {
	dir   string
	pass1 string
	pass2 string
}

// newSecretFiles crea el directorio del trabajo (0700, nombre aleatorio) y escribe las dos
// contrasenas en ficheros 0400. Quien lo llama borra el directorio con remove, pase lo que pase.
func newSecretFiles(root, sourcePass, destPass string) (*secretFiles, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("directorio de trabajo: %w", err)
	}
	dir, err := os.MkdirTemp(root, "job-")
	if err != nil {
		return nil, fmt.Errorf("directorio del trabajo: %w", err)
	}
	s := &secretFiles{dir: dir, pass1: filepath.Join(dir, "source.pass"), pass2: filepath.Join(dir, "dest.pass")}
	if err := writeSecret(s.pass1, sourcePass); err != nil {
		s.remove()
		return nil, err
	}
	if err := writeSecret(s.pass2, destPass); err != nil {
		s.remove()
		return nil, err
	}
	return s, nil
}

func writeSecret(path, value string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return fmt.Errorf("fichero de contrasena: %w", err)
	}
	if _, err := f.WriteString(value); err != nil {
		f.Close()
		return fmt.Errorf("fichero de contrasena: %w", err)
	}
	return f.Close()
}

func (s *secretFiles) remove() {
	if s == nil || s.dir == "" {
		return
	}
	_ = os.RemoveAll(s.dir)
}

// pass describe una pasada de imapsync: la fase que se informa y el destino ya validado.
type passSpec struct {
	job     *ClaimedJob
	target  SourceTarget
	secrets *secretFiles
}

// buildArgs arma la linea de ordenes de imapsync. Todo valor va en la forma --opcion=valor para que
// ninguno se interprete como otra opcion. La conexion al origen es a la IP ya validada y el
// certificado se verifica siempre contra el nombre que dio el usuario. No se pasa ninguna opcion de
// imapsync que borre o mueva correo: el origen no se toca.
func buildArgs(cfg Config, spec passSpec) []string {
	job := spec.job
	args := []string{
		"--host1=" + spec.target.IP.String(),
		"--port1=" + strconv.Itoa(job.Source.Port),
		"--user1=" + job.Source.Username,
		"--passfile1=" + spec.secrets.pass1,
		"--nosslcheck",
	}
	switch job.Source.TLS {
	case "ssl":
		args = append(args, "--ssl1", "--notls1")
		args = append(args, sslArgs("1", spec.target.VerifyName, !spec.target.IsLiteral)...)
	case "starttls":
		args = append(args, "--nossl1", "--tls1")
		args = append(args, sslArgs("1", spec.target.VerifyName, !spec.target.IsLiteral)...)
	default:
		args = append(args, "--nossl1", "--notls1")
	}

	args = append(args,
		"--host2="+cfg.DestHost,
		"--port2="+strconv.Itoa(cfg.DestPort),
		"--user2="+job.Destination.Username+"*"+cfg.MasterUser+"@platform.local",
		"--passfile2="+spec.secrets.pass2,
		"--ssl2", "--notls2",
	)
	args = append(args, sslArgs("2", cfg.DestTLSServerName, true)...)

	args = append(args,
		"--nolog", "--noreleasecheck", "--noid", "--no-modulesversion", "--nofoldersizesatend",
		"--automap", "--addheader",
		"--errorsmax="+strconv.Itoa(errorsMax),
		"--tmpdir="+spec.secrets.dir,
		"--pidfile="+filepath.Join(spec.secrets.dir, "imapsync.pid"),
	)
	if cfg.ClamdAddr != "" {
		args = append(args, "--pipemess="+cfg.Self+" scan-filter")
	}
	return args
}

func sslArgs(side, verifyName string, sni bool) []string {
	args := []string{
		"--sslargs" + side + "=SSL_verify_mode=1",
		"--sslargs" + side + "=SSL_version=" + sourceSSLVersion,
		"--sslargs" + side + "=SSL_verifycn_name=" + verifyName,
	}
	if sni {
		args = append(args, "--sslargs"+side+"=SSL_hostname="+verifyName)
	}
	return args
}

// childEnv es el entorno completo de imapsync: nada se hereda, de modo que ni la clave del
// ejecutor ni ningun otro secreto del proceso llegan al hijo ni a lo que lance.
func childEnv(cfg Config, dir string) []string {
	env := []string{"PATH=" + childPath, "HOME=" + dir, "TMPDIR=" + dir, "LANG=C.UTF-8"}
	if cfg.ClamdAddr != "" {
		env = append(env,
			"MIGRATION_CLAMD_ADDR="+cfg.ClamdAddr,
			"MIGRATION_SCAN_MAX_BYTES="+strconv.FormatInt(cfg.ScanMaxBytes, 10),
			"MIGRATION_SCAN_TIMEOUT="+cfg.ScanTimeout.String(),
		)
	}
	return env
}

// execImapsync lanza imapsync sin shell, con la salida conectada al analizador, y devuelve su
// codigo de salida (-1 si lo mato una senal, que es lo que pasa al cancelar o vencer el plazo).
func execImapsync(ctx context.Context, cfg Config, spec passSpec, parser *outputParser) (int, error) {
	cmd := exec.CommandContext(ctx, cfg.ImapsyncBin, buildArgs(cfg, spec)...)
	cmd.Dir = spec.secrets.dir
	cmd.Env = childEnv(cfg, spec.secrets.dir)
	cmd.Stdout = parser
	cmd.Stderr = parser
	cmd.WaitDelay = waitDelay
	isolateProcess(cmd)

	// Pdeathsig se asocia al hilo que crea el proceso: se fija el hilo hasta que termine.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return -1, err
}
