package domain

import "testing"

func TestIgnoresSuppression(t *testing.T) {
	for _, tc := range []struct {
		name    string
		purpose string
		s       SuppressedRecipient
		want    bool
	}{
		{"baja sola", PurposeDoubleOptIn, SuppressedRecipient{Reason: "unsubscribe", Reasons: []string{"unsubscribe"}}, true},
		{"baja y manual", PurposeDoubleOptIn, SuppressedRecipient{Reason: "unsubscribe", Reasons: []string{"unsubscribe", "manual"}}, false},
		{"manual sola", PurposeDoubleOptIn, SuppressedRecipient{Reason: "manual", Reasons: []string{"manual"}}, false},
		{"rebote duro", PurposeDoubleOptIn, SuppressedRecipient{Reason: "hard_bounce", Reasons: []string{"hard_bounce"}}, false},
		{"queja", PurposeDoubleOptIn, SuppressedRecipient{Reason: "complaint", Reasons: []string{"complaint"}}, false},
		{"sin lista de causas", PurposeDoubleOptIn, SuppressedRecipient{Reason: "unsubscribe"}, false},
		{"principal incoherente", PurposeDoubleOptIn, SuppressedRecipient{Reason: "manual", Reasons: []string{"unsubscribe"}}, false},
		{"sin proposito", "", SuppressedRecipient{Reason: "unsubscribe", Reasons: []string{"unsubscribe"}}, false},
		{"aviso de cuarentena con baja", PurposeQuarantineNotice, SuppressedRecipient{Reason: "unsubscribe", Reasons: []string{"unsubscribe"}}, false},
		{"aviso de cuarentena con rebote", PurposeQuarantineNotice, SuppressedRecipient{Reason: "hard_bounce", Reasons: []string{"hard_bounce"}}, false},
	} {
		if got := IgnoresSuppression(tc.purpose, tc.s); got != tc.want {
			t.Errorf("%s: %v, se esperaba %v", tc.name, got, tc.want)
		}
	}
}

func TestValidPurpose(t *testing.T) {
	for purpose, want := range map[string]bool{
		"":                      true,
		PurposeDoubleOptIn:      true,
		PurposeQuarantineNotice: true,
		"marketing":             false,
		"QUARANTINE_NOTICE":     false,
		" quarantine_notice":    false,
	} {
		if got := ValidPurpose(purpose); got != want {
			t.Errorf("%q: %v, se esperaba %v", purpose, got, want)
		}
	}
}

func TestIsTestSend(t *testing.T) {
	for tags, want := range map[*map[string]string]bool{
		{"test": "true"}:     true,
		{"test": "false"}:    false,
		{"test": "TRUE"}:     false,
		{"campaign": "true"}: false,
		{}:                   false,
	} {
		if got := IsTestSend(*tags); got != want {
			t.Errorf("%v: %v, se esperaba %v", *tags, got, want)
		}
	}
	if IsTestSend(nil) {
		t.Error("sin etiquetas no es prueba")
	}
}
