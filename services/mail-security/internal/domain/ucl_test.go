package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestRenderSettingsSiempreLlevaElWatchdog(t *testing.T) {
	out := RenderSettings(SettingsInput{})
	for _, want := range []string{
		"settings {",
		"  watchdog {",
		"    priority = 10;",
		`    rcpt_mime = "/null@localhost/i";`,
		`    from_mime = "/watchdog@localhost/i";`,
		"        reject = 9999.0;",
		"      want_spam = yes;",
		`"HISTORY_SAVE", "ARC", "ARC_SIGNED", "DKIM", "DKIM_SIGNED", "CLAM_VIRUS"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("falta %q en:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "}\n") {
		t.Fatalf("el documento no cierra settings: %q", out)
	}
}

func TestRenderSettingsUmbralPorBuzonYPorDominio(t *testing.T) {
	out := RenderSettings(SettingsInput{Scores: []ScoreRule{
		{Object: "ana@acme.com", Kind: ObjectMailbox, Recipients: []string{"ana@acme-alias.com", "ventas@acme.com"},
			HighScore: decimal.RequireFromString("12.5"), LowScore: decimal.RequireFromString("6")},
		{Object: "acme.com", Kind: ObjectDomain, Recipients: []string{"acme-alias.com"},
			HighScore: decimal.NewFromInt(20), LowScore: decimal.NewFromInt(10)},
	}})
	for _, want := range []string{
		`rcpt = ["/^ana@acme[.]com$/i", "/^ana@acme-alias[.]com$/i", "/^ventas@acme[.]com$/i"];`,
		"        reject = 12.50;",
		"        add_header = 6.00;",
		`rcpt = ["/@acme[.]com$/i", "/@acme-alias[.]com$/i"];`,
		"        reject = 20.00;",
		"        add_header = 10.00;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("falta %q en:\n%s", want, out)
		}
	}
}

func TestRenderSettingsListasConSimbolosDeRspamd(t *testing.T) {
	out := RenderSettings(SettingsInput{Lists: []ListRule{
		{Object: "acme.com", Kind: ObjectDomain, ListKind: ListAllow, Patterns: []string{"@partner.com", "*@news.example.org", "juan+x@ok.com"}},
		{Object: "ana@acme.com", Kind: ObjectMailbox, ListKind: ListDeny, Patterns: []string{"spam@bad.com"}},
	}})
	for _, want := range []string{
		"  allow_0 {",
		`    from = ["/^.*@partner[.]com$/i", "/^.*@news[.]example[.]org$/i", "/^juan[+]x@ok[.]com$/i"];`,
		"      MAILCOW_WHITE = -999.0;",
		`      "MAILCOW_WHITE"`,
		"  deny_1 {",
		`    from = ["/^spam@bad[.]com$/i"];`,
		"      MAILCOW_BLACK = 999.0;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("falta %q en:\n%s", want, out)
		}
	}
}

func TestRenderSettingsEscapaYDescartaLoQueRomperiaElUCL(t *testing.T) {
	out := RenderSettings(SettingsInput{Lists: []ListRule{
		{Object: "acme.com", Kind: ObjectDomain, ListKind: ListAllow, Patterns: []string{`evil"@x.com`, "@ok.com"}},
	}})
	if strings.Contains(out, `"`+`evil`) || strings.Contains(out, `\"`) {
		t.Fatalf("un patron invalido no debe llegar al UCL:\n%s", out)
	}
	if !strings.Contains(out, `"/^.*@ok[.]com$/i"`) {
		t.Fatalf("el patron valido debe conservarse:\n%s", out)
	}
	if strings.Contains(out, `\`) {
		t.Fatalf("el UCL generado no debe contener barras invertidas:\n%s", out)
	}
}

func TestRenderSettingsPegaLosBloquesAdicionalesIndentados(t *testing.T) {
	out := RenderSettings(SettingsInput{Maps: []string{"custom_rule {\n  priority = 1;\n  from = \"x@y.com\";\n}"}})
	if !strings.Contains(out, "  custom_rule {\n    priority = 1;\n") {
		t.Fatalf("bloque adicional mal pegado:\n%s", out)
	}
}

func TestValidateSettingsMapContent(t *testing.T) {
	cases := map[string]bool{
		"rule { priority = 1; }":        true,
		"settings { rule { } }":         false,
		"settings   {":                  false,
		"rule { ":                       false,
		"rule } {":                      false,
		"":                              false,
		"rule {\n  apply { x = 1; }\n}": true,
	}
	for content, ok := range cases {
		err := ValidateSettingsMapContent(content)
		if ok && err != nil {
			t.Errorf("%q deberia ser valido: %v", content, err)
		}
		if !ok && err == nil {
			t.Errorf("%q deberia rechazarse", content)
		}
	}
}

func TestRenderSettingsAliasInternoSoloAdmiteSuOrganizacion(t *testing.T) {
	out := RenderSettings(SettingsInput{InternalAliases: []InternalAliasRule{
		{Address: "RRHH@acme.test", Domains: []string{"acme.test", "acme-alias.test", "acme.test"}},
		{Address: "@interno.test", Domains: []string{"interno.test"}},
		{Address: "sin-dominios@x.test"},
	}})
	for _, want := range []string{
		"internal_alias_0 {",
		`rcpt = ["/^rrhh@acme[.]test$/i", "/^rrhh@acme-alias[.]test$/i"];`,
		`from = "/^(?!.*@(acme[.]test|acme-alias[.]test)$).*$/i";`,
		SymbolInternalAlias + " = 9999.0;",
		"internal_alias_1 {",
		`rcpt = ["/@interno[.]test$/i"];`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("falta %q en:\n%s", want, out)
		}
	}
	if strings.Contains(out, "internal_alias_2") {
		t.Fatal("un alias sin dominios no debe generar regla")
	}
	if strings.Index(out, "watchdog {") > strings.Index(out, "internal_alias_0") {
		t.Fatal("el watchdog va primero")
	}
}
