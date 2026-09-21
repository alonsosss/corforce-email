// Command migration-runner es el ejecutor de la migracion de buzones: reclama trabajos a
// mail-migration, valida el origen y copia el correo con imapsync hacia el IMAP de Dovecot.
//
// Corre en una red propia, sin acceso a la base, a Redis ni a NATS: solo habla con la API del
// ejecutor de mail-migration, con Dovecot, con clamd y con el servidor IMAP de origen. Sin
// MAIL_MIGRATION_RUNNER_KEY arranca, lo avisa y no reclama nada (y no termina: el contenedor no
// se reinicia en bucle). Con la configuracion incompleta no reclama y queda no sano.
//
// Subcomandos:
//
//	migration-runner              ejecutor
//	migration-runner scan-filter  filtro antivirus que imapsync ejecuta por cada mensaje (--pipemess)
//	migration-runner healthcheck  comprobacion de salud del contenedor
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const (
	healthFile     = ".alive"
	healthInterval = 10 * time.Second
	healthMaxAge   = 45 * time.Second
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "scan-filter":
			os.Exit(runScanFilter(os.Stdin, os.Stdout, os.Stderr, scanConfigFromEnv(os.Getenv)))
		case "healthcheck":
			os.Exit(runHealthcheck(os.Getenv))
		default:
			fmt.Fprintln(os.Stderr, "uso: migration-runner [scan-filter|healthcheck]")
			os.Exit(2)
		}
	}
	os.Exit(run())
}

func run() int {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", "migration-runner")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Los ficheros con contrasenas y los temporales de imapsync nacen 0600 y no hay volcado de memoria.
	syscall.Umask(0o077)
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{})

	cfg, state, err := LoadConfig(os.Getenv)
	switch state {
	case StateMisconfigured:
		log.Error("configuracion invalida: el ejecutor no reclamara trabajos", "error", err)
		<-ctx.Done()
		return 0
	case StateDisabled:
		log.Warn("migracion desactivada: falta MAIL_MIGRATION_RUNNER_KEY; el ejecutor no reclama trabajos")
		markHealthy(ctx, cfg.WorkDir, log)
		<-ctx.Done()
		return 0
	}

	if cfg.AllowUnscanned && cfg.ClamdAddr == "" {
		log.Warn("MIGRATION_ALLOW_UNSCANNED=true y sin clamd: el correo migrado NO se analiza con el antivirus")
	}
	if cfg.AllowPrivateSources {
		log.Warn("MIGRATION_ALLOW_PRIVATE_SOURCES activo (entorno " + cfg.Environment + "): se admiten servidores de origen en redes privadas")
	}
	if err := prepareWorkDir(cfg.WorkDir); err != nil {
		log.Error("directorio de trabajo inutilizable: el ejecutor no reclamara trabajos", "error", err)
		<-ctx.Done()
		return 0
	}
	markHealthy(ctx, cfg.WorkDir, log)

	api := NewAPI(cfg.APIURL, cfg.RunnerKey, cfg.RunnerID)
	runner := NewRunner(cfg, api, NewSourceGuard(defaultResolver(), cfg.AllowPrivateSources), log)
	log.Info("ejecutor de migracion listo", "runner_id", cfg.RunnerID, "antivirus", cfg.ClamdAddr != "")
	runner.Loop(ctx)
	return 0
}

// prepareWorkDir deja el directorio de trabajo vacio y privado. Lo que quede de un proceso anterior
// (un trabajo que murio a medias) no debe sobrevivir al arranque.
func prepareWorkDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// markHealthy escribe la marca de vida que lee healthcheck mientras el proceso este activo.
func markHealthy(ctx context.Context, dir string, log *slog.Logger) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Warn("no se pudo preparar la marca de salud", "error", err)
		return
	}
	path := filepath.Join(dir, healthFile)
	touch := func() { _ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)), 0o600) }
	touch()
	go func() {
		t := time.NewTicker(healthInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				touch()
			}
		}
	}()
}

func runHealthcheck(getenv func(string) string) int {
	dir := getenv("MIGRATION_WORK_DIR")
	if dir == "" {
		dir = defaultWorkDir
	}
	info, err := os.Stat(filepath.Join(dir, healthFile))
	if err != nil || time.Since(info.ModTime()) > healthMaxAge {
		return 1
	}
	return 0
}
