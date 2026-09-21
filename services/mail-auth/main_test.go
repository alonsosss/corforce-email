package main

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestSinConfiguracionLasCredencialesDeTrabajoQuedanDesactivadas(t *testing.T) {
	t.Setenv(migrationURLEnv, "")
	t.Setenv(jobNetworksEnv, "")
	jobs, err := jobCredentialsFromEnv(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if jobs.verifier != nil || len(jobs.networks) != 0 {
		t.Fatalf("debe quedar desactivado: %+v", jobs)
	}
}

func TestLaConfiguracionIncompletaImpideArrancar(t *testing.T) {
	t.Setenv("INTERNAL_GATEWAY_TOKEN", strings.Repeat("g", 40))
	for name, env := range map[string][2]string{
		"solo la URL":               {"http://mail-migration:8056", ""},
		"solo la red":               {"", "172.22.2.0/24"},
		"URL invalida":              {"mail-migration:8056", "172.22.2.0/24"},
		"URL con ruta":              {"http://mail-migration:8056/x", "172.22.2.0/24"},
		"red invalida":              {"http://mail-migration:8056", "172.22.2.5"},
		"red abierta":               {"http://mail-migration:8056", "0.0.0.0/0"},
		"red v6 abierta":            {"http://mail-migration:8056", "::/0"},
		"una red mala entre buenas": {"http://mail-migration:8056", "172.22.2.0/24,basura"},
	} {
		t.Setenv(migrationURLEnv, env[0])
		t.Setenv(jobNetworksEnv, env[1])
		if _, err := jobCredentialsFromEnv(zap.NewNop()); err == nil {
			t.Errorf("%s: arranco con configuracion incoherente", name)
		}
	}
}

func TestLaConfiguracionCompletaActivaLasCredencialesDeTrabajo(t *testing.T) {
	t.Setenv("INTERNAL_GATEWAY_TOKEN", strings.Repeat("g", 40))
	t.Setenv(migrationURLEnv, "http://mail-migration:8056")
	t.Setenv(jobNetworksEnv, " 172.22.2.0/24 , fd00:2::/64 ")
	jobs, err := jobCredentialsFromEnv(zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if jobs.verifier == nil || len(jobs.networks) != 2 || jobs.networks[0].String() != "172.22.2.0/24" {
		t.Fatalf("configuracion: %+v", jobs)
	}
}

func TestLaRedSeNormalizaASuPrefijo(t *testing.T) {
	nets, err := parseNetworks("172.22.2.77/24")
	if err != nil || len(nets) != 1 || nets[0].String() != "172.22.2.0/24" {
		t.Fatalf("redes: %v %v", nets, err)
	}
}
