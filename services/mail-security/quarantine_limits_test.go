package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
)

// Los techos de la retencion de cuarentena viven a la vez en el dominio (los exige
// PutQuarantineSettings) y en el CHECK de la migracion de celda (los exige la base). Si dejan de
// decir lo mismo, una empresa puede fijar por API un valor que la base rechaza, o al reves queda
// una fila que el servicio nunca habria aceptado. Esta prueba los ata, como
// ops/scaffold/check-mail-size-limits.sh ata el techo de max_size_bytes de la migracion 08.
func TestTechosDeRetencionDeCuarentenaCoincidenConLaMigracion(t *testing.T) {
	ruta := filepath.Join("..", "..", "migrations", "cell", "canonical", "mail-security", "09_quarantine_retention.sql")
	sql, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("migracion 09: %v", err)
	}
	cases := []struct {
		nombre string
		re     *regexp.Regexp
		want   int
	}{
		{"CHECK de retention_size", regexp.MustCompile(`CHECK \(retention_size >= 0 AND retention_size <= (\d+)\)`), domain.MaxQuarantineRetentionSize},
		{"recorte de retention_size", regexp.MustCompile(`SET retention_size = (\d+)`), domain.MaxQuarantineRetentionSize},
		{"CHECK de max_age_days", regexp.MustCompile(`CHECK \(max_age_days > 0 AND max_age_days <= (\d+)\)`), domain.MaxQuarantineMaxAgeDays},
		{"recorte de max_age_days", regexp.MustCompile(`SET max_age_days = (\d+)`), domain.MaxQuarantineMaxAgeDays},
		{"CHECK de exclude_domains", regexp.MustCompile(`jsonb_array_length\(exclude_domains\) <= (\d+)`), domain.MaxQuarantineExcludeDomains},
	}
	for _, c := range cases {
		m := c.re.FindSubmatch(sql)
		if m == nil {
			t.Errorf("%s: no se encuentra en %s (patron %s)", c.nombre, ruta, c.re)
			continue
		}
		got, err := strconv.Atoi(string(m[1]))
		if err != nil || got != c.want {
			t.Errorf("%s = %s (%v); el dominio dice %d", c.nombre, m[1], err, c.want)
		}
	}
	// El recorte de exclude_domains usa un rango de jsonpath [0 to N-1], no el mismo numero.
	rango := regexp.MustCompile(`\$\[0 to (\d+)\]`).FindSubmatch(sql)
	if rango == nil {
		t.Fatalf("no se encuentra el recorte de exclude_domains en %s", ruta)
	}
	if got, err := strconv.Atoi(string(rango[1])); err != nil || got != domain.MaxQuarantineExcludeDomains-1 {
		t.Errorf("el recorte deja $[0 to %s]; con un techo de %d debe ser %d", rango[1],
			domain.MaxQuarantineExcludeDomains, domain.MaxQuarantineExcludeDomains-1)
	}
}
