package config

import (
	"os"
	"testing"
)

func TestDeclaredDevelopmentOrTest(t *testing.T) {
	cases := map[string]bool{
		"development":            true,
		"test":                   true,
		"Development":            true,
		"TEST":                   true,
		" development ":          true,
		"production":             false,
		"Production":             false,
		"staging":                false,
		"":                       false,
		"dev":                    false,
		"testing":                false,
		"development1":           false,
		"production,development": false,
	}
	for environment, want := range cases {
		t.Setenv("ENVIRONMENT", environment)
		if got := DeclaredDevelopmentOrTest(); got != want {
			t.Errorf("ENVIRONMENT=%q: %v, se esperaba %v", environment, got, want)
		}
	}

	t.Setenv("ENVIRONMENT", "development")
	if err := os.Unsetenv("ENVIRONMENT"); err != nil {
		t.Fatal(err)
	}
	if DeclaredDevelopmentOrTest() {
		t.Error("sin ENVIRONMENT no se relaja nada")
	}
}
