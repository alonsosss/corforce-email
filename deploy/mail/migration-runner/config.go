package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	minSecretLength  = 32
	defaultDestPort  = 993
	defaultJobLimit  = 24 * time.Hour
	defaultPoll      = 15 * time.Second
	defaultWorkDir   = "/run/migration"
	defaultImapsync  = "/usr/bin/imapsync"
	defaultScanMax   = 100 << 20
	defaultScanLimit = 5 * time.Minute
)

// executable se sustituye en las pruebas: la ruta del binario de prueba no la controla el repositorio.
var executable = os.Executable

var masterUserPattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

// State es lo que el ejecutor puede hacer segun su configuracion. Disabled es un estado normal
// (migracion sin activar en esta celda); Misconfigured es un error del operador y deja el
// contenedor no sano para que se vea, sin reiniciarlo en bucle.
type State int

const (
	StateActive State = iota
	StateDisabled
	StateMisconfigured
)

type Config struct {
	Environment         string
	AllowPrivateSources bool
	AllowUnscanned      bool

	APIURL    *url.URL
	RunnerKey string
	RunnerID  string

	DestHost          string
	DestPort          int
	DestTLSServerName string
	MasterUser        string
	MasterPass        string

	ClamdAddr    string
	ScanMaxBytes int64
	ScanTimeout  time.Duration

	JobTimeout   time.Duration
	PollInterval time.Duration

	ImapsyncBin string
	WorkDir     string
	Self        string
}

// LoadConfig lee la configuracion del entorno. La guarda de las fuentes privadas se comprueba
// siempre, incluso sin clave: una celda de produccion con MIGRATION_ALLOW_PRIVATE_SOURCES no
// debe quedar a la espera de que alguien ponga la clave. El resto solo se exige cuando hay clave.
func LoadConfig(getenv func(string) string) (Config, State, error) {
	env := func(name string) string { return strings.TrimSpace(getenv(name)) }
	cfg := Config{
		Environment: strings.ToLower(env("ENVIRONMENT")),
		RunnerKey:   env("MAIL_MIGRATION_RUNNER_KEY"),
		ImapsyncBin: envOr(env, "MIGRATION_IMAPSYNC_BIN", defaultImapsync),
		WorkDir:     envOr(env, "MIGRATION_WORK_DIR", defaultWorkDir),
	}
	var errs []error

	var err error
	if cfg.AllowPrivateSources, err = envBool(env, "MIGRATION_ALLOW_PRIVATE_SOURCES"); err != nil {
		errs = append(errs, err)
	}
	if cfg.AllowUnscanned, err = envBool(env, "MIGRATION_ALLOW_UNSCANNED"); err != nil {
		errs = append(errs, err)
	}
	if cfg.AllowPrivateSources && cfg.Environment != "development" && cfg.Environment != "test" {
		errs = append(errs, errors.New("MIGRATION_ALLOW_PRIVATE_SOURCES solo se admite con ENVIRONMENT development o test"))
	}
	if len(errs) > 0 {
		return cfg, StateMisconfigured, errors.Join(errs...)
	}
	if cfg.RunnerKey == "" {
		return cfg, StateDisabled, nil
	}

	if len(cfg.RunnerKey) < minSecretLength {
		errs = append(errs, fmt.Errorf("MAIL_MIGRATION_RUNNER_KEY debe tener al menos %d caracteres", minSecretLength))
	}
	if cfg.APIURL, err = parseAPIURL(env("MIGRATION_API_URL")); err != nil {
		errs = append(errs, err)
	}
	cfg.RunnerID = envOr(env, "MIGRATION_RUNNER_ID", hostname())

	cfg.DestHost = env("MIGRATION_DEST_HOST")
	if cfg.DestHost == "" {
		errs = append(errs, errors.New("falta MIGRATION_DEST_HOST"))
	}
	if cfg.DestPort, err = envInt(env, "MIGRATION_DEST_PORT", defaultDestPort, 1, 65535); err != nil {
		errs = append(errs, err)
	}
	cfg.DestTLSServerName = env("MIGRATION_DEST_TLS_SERVER_NAME")
	if cfg.DestTLSServerName == "" {
		errs = append(errs, errors.New("falta MIGRATION_DEST_TLS_SERVER_NAME: el certificado de Dovecot se verifica siempre"))
	}

	cfg.MasterUser = env("DOVECOT_MIGRATION_MASTER_USER")
	cfg.MasterPass = env("DOVECOT_MIGRATION_MASTER_PASS")
	if !masterUserPattern.MatchString(cfg.MasterUser) {
		errs = append(errs, errors.New("DOVECOT_MIGRATION_MASTER_USER falta o no cumple [a-z0-9._-]"))
	}
	if len(cfg.MasterPass) < minSecretLength {
		errs = append(errs, fmt.Errorf("DOVECOT_MIGRATION_MASTER_PASS falta o tiene menos de %d caracteres", minSecretLength))
	}

	cfg.ClamdAddr = env("MIGRATION_CLAMD_ADDR")
	if cfg.ClamdAddr == "" && !cfg.AllowUnscanned {
		errs = append(errs, errors.New("falta MIGRATION_CLAMD_ADDR: sin analisis antivirus no se reclama ningun trabajo salvo MIGRATION_ALLOW_UNSCANNED=true"))
	}
	if cfg.ClamdAddr != "" {
		if _, _, splitErr := net.SplitHostPort(cfg.ClamdAddr); splitErr != nil {
			errs = append(errs, errors.New("MIGRATION_CLAMD_ADDR debe ser host:puerto"))
		}
	}
	if cfg.ScanMaxBytes, err = envInt64(env, "MIGRATION_SCAN_MAX_BYTES", defaultScanMax, 1<<20, 1<<30); err != nil {
		errs = append(errs, err)
	}
	if cfg.ScanTimeout, err = envDuration(env, "MIGRATION_SCAN_TIMEOUT", defaultScanLimit, time.Second, time.Hour); err != nil {
		errs = append(errs, err)
	}

	if cfg.JobTimeout, err = envDuration(env, "MIGRATION_JOB_TIMEOUT", defaultJobLimit, time.Minute, 72*time.Hour); err != nil {
		errs = append(errs, err)
	}
	if cfg.PollInterval, err = envDuration(env, "MIGRATION_POLL_INTERVAL", defaultPoll, time.Second, 10*time.Minute); err != nil {
		errs = append(errs, err)
	}

	if !filepath.IsAbs(cfg.ImapsyncBin) || !safeShellPath.MatchString(cfg.WorkDir) {
		errs = append(errs, errors.New("MIGRATION_IMAPSYNC_BIN debe ser una ruta absoluta y MIGRATION_WORK_DIR una ruta absoluta sin caracteres especiales"))
	}
	if cfg.Self, err = executable(); err != nil {
		errs = append(errs, fmt.Errorf("no se pudo localizar el propio binario: %w", err))
	} else if !safeShellPath.MatchString(cfg.Self) {
		errs = append(errs, errors.New("la ruta del binario contiene caracteres que imapsync pasaria por el shell"))
	}

	if len(errs) > 0 {
		return cfg, StateMisconfigured, errors.Join(errs...)
	}
	return cfg, StateActive, nil
}

// safeShellPath acota la ruta del propio binario: imapsync ejecuta --pipemess con un shell para las
// redirecciones, asi que la ruta no puede llevar nada que el shell interprete.
var safeShellPath = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)

func parseAPIURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, errors.New("falta MIGRATION_API_URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("MIGRATION_API_URL debe ser http(s)://host:puerto sin credenciales ni parametros")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func envOr(env func(string) string, name, fallback string) string {
	if v := env(name); v != "" {
		return v
	}
	return fallback
}

func envBool(env func(string) string, name string) (bool, error) {
	switch strings.ToLower(env(name)) {
	case "", "false", "0", "no":
		return false, nil
	case "true", "1", "yes":
		return true, nil
	}
	return false, fmt.Errorf("%s debe ser true o false", name)
}

func envInt(env func(string) string, name string, fallback, lo, hi int) (int, error) {
	v := env(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return fallback, fmt.Errorf("%s debe ser un entero entre %d y %d", name, lo, hi)
	}
	return n, nil
}

func envInt64(env func(string) string, name string, fallback, lo, hi int64) (int64, error) {
	v := env(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < lo || n > hi {
		return fallback, fmt.Errorf("%s debe ser un entero entre %d y %d", name, lo, hi)
	}
	return n, nil
}

func envDuration(env func(string) string, name string, fallback, lo, hi time.Duration) (time.Duration, error) {
	v := env(name)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d > hi {
		return fallback, fmt.Errorf("%s debe ser una duracion entre %s y %s", name, lo, hi)
	}
	return d, nil
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "migration-runner"
}
