package domain

import (
	"errors"
	"testing"
)

func TestCheckGrantNoResuscribePorAPI(t *testing.T) {
	ip := "203.0.113.7"
	cases := []struct {
		status  Status
		consent ConsentStatus
		method  ConsentMethod
		ip      *string
		ok      bool
	}{
		{StatusActive, ConsentNone, MethodAPI, nil, true},
		{StatusActive, ConsentPending, MethodAPI, nil, true},
		{StatusBounced, ConsentNone, MethodAPI, nil, true},
		// Quien se dio de baja solo vuelve con prueba de que lo pidio el mismo.
		{StatusUnsubscribed, ConsentRevoked, MethodAPI, nil, false},
		{StatusUnsubscribed, ConsentRevoked, MethodAPI, &ip, false},
		{StatusUnsubscribed, ConsentRevoked, MethodForm, nil, false},
		{StatusUnsubscribed, ConsentRevoked, MethodForm, &ip, true},
		{StatusUnsubscribed, ConsentRevoked, MethodDoubleOptIn, nil, true},
		// Retirar el consentimiento pesa lo mismo que la baja.
		{StatusActive, ConsentRevoked, MethodAPI, nil, false},
		{StatusActive, ConsentRevoked, MethodImport, nil, false},
		{StatusActive, ConsentRevoked, MethodForm, &ip, true},
	}
	for _, tc := range cases {
		c := &Contact{Status: tc.status, ConsentStatus: tc.consent}
		err := CheckGrant(c, tc.method, tc.ip)
		if tc.ok && err != nil {
			t.Errorf("%+v: se esperaba permitido, hubo %v", tc, err)
		}
		if !tc.ok && !errors.Is(err, ErrResubscribeRequiresOptIn) {
			t.Errorf("%+v: se esperaba ErrResubscribeRequiresOptIn, hubo %v", tc, err)
		}
	}
}

func TestParseoDeConsentimientoPorAPI(t *testing.T) {
	for _, m := range []string{"double_opt_in", "import", "suppression", "unsubscribe_link", ""} {
		if _, err := ParseAPIMethod(m); !errors.Is(err, ErrInvalidConsentMethod) {
			t.Errorf("%q no debe aceptarse por API: %v", m, err)
		}
	}
	if _, err := ParseGrantStatus("pending"); !errors.Is(err, ErrInvalidConsentStatus) {
		t.Fatal("pending no se declara por API: lo crea el doble opt-in")
	}
	if ip, err := NormalizeIP(" 2001:db8::1 "); err != nil || *ip != "2001:db8::1" {
		t.Fatalf("ip: %v %v", ip, err)
	}
	if _, err := NormalizeIP("999.1.1.1"); !errors.Is(err, ErrInvalidIP) {
		t.Fatal("ip invalida")
	}
}

func TestCheckConfirmationRequest(t *testing.T) {
	if err := CheckConfirmationRequest(&Contact{Status: StatusBounced}); !errors.Is(err, ErrContactNotReachable) {
		t.Fatalf("rebote: %v", err)
	}
	if err := CheckConfirmationRequest(&Contact{Status: StatusComplained}); !errors.Is(err, ErrContactNotReachable) {
		t.Fatalf("queja: %v", err)
	}
	if err := CheckConfirmationRequest(&Contact{Status: StatusActive, ConsentStatus: ConsentGranted}); !errors.Is(err, ErrConsentAlreadyGranted) {
		t.Fatalf("ya concedido: %v", err)
	}
	for _, c := range []*Contact{
		{Status: StatusActive, ConsentStatus: ConsentNone},
		{Status: StatusUnsubscribed, ConsentStatus: ConsentRevoked},
		{Status: StatusActive, ConsentStatus: ConsentPending},
	} {
		if err := CheckConfirmationRequest(c); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func TestEstadosPorSupresion(t *testing.T) {
	c := &Contact{Status: StatusActive}
	if !c.ApplySuppression(StatusUnsubscribed) || c.Status != StatusUnsubscribed {
		t.Fatal("una baja sobre activo cambia el estado")
	}
	if !c.ApplySuppression(StatusBounced) || c.Status != StatusBounced {
		t.Fatal("un rebote pesa mas que una baja")
	}
	if c.ApplySuppression(StatusUnsubscribed) || c.Status != StatusBounced {
		t.Fatal("una baja no degrada un rebote")
	}
	if c.ApplySuppression(StatusBounced) {
		t.Fatal("el mismo estado no es un cambio")
	}
	if !c.ApplySuppression(StatusComplained) {
		t.Fatal("una queja pesa mas que un rebote")
	}
	if c.LiftSuppression(StatusBounced) || c.Status != StatusComplained {
		t.Fatal("retirar el rebote no levanta una queja")
	}
	if !c.LiftSuppression(StatusComplained) || c.Status != StatusActive {
		t.Fatal("retirar la queja reactiva")
	}
	u := &Contact{Status: StatusUnsubscribed}
	if u.LiftSuppression(StatusUnsubscribed) {
		t.Fatal("una baja solo la levanta un consentimiento nuevo")
	}
	if !u.Reactivate() || u.Status != StatusActive {
		t.Fatal("Reactivate debe volver a active a quien se dio de baja")
	}
	if (&Contact{Status: StatusBounced}).Reactivate() {
		t.Fatal("un rebote no se levanta por consentir")
	}
}

func TestImportMayGrant(t *testing.T) {
	cases := []struct {
		status  Status
		consent ConsentStatus
		want    bool
	}{
		{StatusActive, ConsentNone, true},
		{StatusActive, ConsentPending, true},
		{StatusActive, ConsentGranted, false},
		{StatusActive, ConsentRevoked, false},
		{StatusUnsubscribed, ConsentNone, false},
		{StatusBounced, ConsentNone, false},
		{StatusComplained, ConsentPending, false},
	}
	for _, tc := range cases {
		if got := ImportMayGrant(&Contact{Status: tc.status, ConsentStatus: tc.consent}); got != tc.want {
			t.Errorf("%s/%s: %v, se esperaba %v", tc.status, tc.consent, got, tc.want)
		}
	}
}
