package maildirectorycli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

const apID = "33333333-3333-4333-8333-333333333333"

func TestVerificacionEnDosPasosContraElDirectorio(t *testing.T) {
	c, calls := fakeDirectory(t, map[string]reply{
		"GET " + mfaPath:          {200, `{"data":{"enabled":true,"enabled_at":"2026-09-24T10:00:00Z","recovery_remaining":8}}`},
		"POST " + mfaActivatePath: {201, `{"data":{"recovery_codes":["AAAAA-BBBBB","CCCCC-DDDDD"]}}`},
		"POST " + mfaVerifyPath:   {200, `{"data":{"method":"recovery","recovery_remaining":7}}`},
		"POST " + mfaRecoveryPath: {200, `{"data":{"recovery_codes":["EEEEE-FFFFF"]}}`},
		"DELETE " + mfaPath:       {204, ``},
	})
	ctx := context.Background()
	st, err := c.MFAStatus(ctx, "ana@empresa.pe")
	if err != nil || !st.Enabled || st.EnabledAt == nil || st.RecoveryRemaining != 8 {
		t.Fatalf("estado: %+v %v", st, err)
	}
	codes, err := c.ActivateMFA(ctx, "ana@empresa.pe", "JBSWY3DPEHPK3PXP", "123456")
	if err != nil || len(codes) != 2 {
		t.Fatalf("activar: %v %v", codes, err)
	}
	v, err := c.VerifyMFA(ctx, "ana@empresa.pe", "ABCDE-FGHIJ")
	if err != nil || v.Method != "recovery" || v.RecoveryRemaining != 7 {
		t.Fatalf("verificar: %+v %v", v, err)
	}
	if codes, err := c.RegenerateRecoveryCodes(ctx, "ana@empresa.pe", "123456"); err != nil || len(codes) != 1 {
		t.Fatalf("regenerar: %v %v", codes, err)
	}
	if err := c.DisableMFA(ctx, "ana@empresa.pe", "654321"); err != nil {
		t.Fatalf("desactivar: %v", err)
	}

	want := []struct {
		method, path string
		body         map[string]any
	}{
		{"GET", mfaPath, nil},
		{"POST", mfaActivatePath, map[string]any{"secret": "JBSWY3DPEHPK3PXP", "code": "123456"}},
		{"POST", mfaVerifyPath, map[string]any{"code": "ABCDE-FGHIJ"}},
		{"POST", mfaRecoveryPath, map[string]any{"code": "123456"}},
		{"DELETE", mfaPath, map[string]any{"code": "654321"}},
	}
	if len(*calls) != len(want) {
		t.Fatalf("llamadas: %+v", *calls)
	}
	for i, w := range want {
		got := (*calls)[i]
		if got.method != w.method || got.path != w.path || got.query != "username=ana%40empresa.pe" || got.header.Get("X-Gateway-Token") != "token-interno" {
			t.Fatalf("llamada %d: %+v", i, got)
		}
		if len(got.body) != len(w.body) {
			t.Fatalf("cuerpo %d: %v", i, got.body)
		}
		for k, v := range w.body {
			if got.body[k] != v {
				t.Fatalf("cuerpo %d: %v", i, got.body)
			}
		}
	}
}

func TestRechazosDeLaVerificacion(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   error
	}{
		{422, `{"error":{"code":"INVALID_MFA_CODE","message":"el código no es válido"}}`, domain.ErrInvalidMFACode},
		{409, `{"error":{"code":"MFA_NOT_ENABLED","message":"no activa"}}`, domain.ErrMFANotEnabled},
		{409, `{"error":{"code":"MFA_ALREADY_ENABLED","message":"ya activa"}}`, domain.ErrMFAAlreadyEnabled},
		{500, `{"error":{"code":"INTERNAL","message":"x"}}`, domain.ErrUnavailable},
		{200, `{"data":{"method":"sms"}}`, domain.ErrUnavailable},
	} {
		client, _ := fakeDirectory(t, map[string]reply{"POST " + mfaVerifyPath: {c.status, c.body}})
		if _, err := client.VerifyMFA(context.Background(), "ana@empresa.pe", "123456"); !errors.Is(err, c.want) {
			t.Fatalf("%d %s: %v", c.status, c.body, err)
		}
	}
	client, _ := fakeDirectory(t, map[string]reply{"POST " + mfaActivatePath: {201, `{"data":{"recovery_codes":[]}}`}})
	if _, err := client.ActivateMFA(context.Background(), "ana@empresa.pe", "S", "123456"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("sin codigos de recuperacion: %v", err)
	}
	client, _ = fakeDirectory(t, map[string]reply{"POST " + mfaActivatePath: {422, `{"error":{"code":"VALIDATION_ERROR","message":"secreto invalido","details":{"field":"secret"}}}`}})
	var verr *domain.ValidationError
	if _, err := client.ActivateMFA(context.Background(), "ana@empresa.pe", "S", "123456"); !errors.As(err, &verr) || verr.Field != "secret" {
		t.Fatalf("validacion: %v", err)
	}
}

func TestContrasenasDeAplicacionContraElDirectorio(t *testing.T) {
	row := `{"id":"` + strings.ToUpper(apID) + `","tenant_id":"t","mailbox_id":"m","name":"Movil","imap_access":true,"pop3_access":false,"smtp_access":true,"sieve_access":false,"dav_access":true,"active":true,"last_used_at":null,"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-01T00:00:00Z"}`
	c, calls := fakeDirectory(t, map[string]reply{
		"GET " + appPasswordsPath:                 {200, `{"data":[` + row + `]}`},
		"POST " + appPasswordsPath:                {201, `{"data":{"app_password":` + row + `,"password":"abcd-efgh"}}`},
		"DELETE " + appPasswordsPath + "/" + apID: {204, ``},
	})
	ctx := context.Background()
	list, err := c.AppPasswords(ctx, "ana@empresa.pe")
	if err != nil || len(list.Items) != 1 || list.Max != 0 {
		t.Fatalf("lista: %+v %v", list, err)
	}
	p := list.Items[0]
	if p.ID != apID || p.Name != "Movil" || !p.Access.IMAP || p.Access.POP3 || !p.Access.SMTP || !p.Access.DAV || !p.Active {
		t.Fatalf("fila: %+v", p)
	}
	created, err := c.CreateAppPassword(ctx, "ana@empresa.pe", domain.AppPasswordInput{Name: "Movil", Access: domain.AppPasswordAccess{IMAP: true, SMTP: true, DAV: true}})
	if err != nil || created.Password != "abcd-efgh" || created.ID != apID {
		t.Fatalf("crear: %+v %v", created, err)
	}
	post := (*calls)[1]
	wantBody := map[string]any{"name": "Movil", "imap": true, "pop3": false, "smtp": true, "sieve": false, "dav": true}
	for k, v := range wantBody {
		if post.body[k] != v {
			t.Fatalf("cuerpo del alta: %v", post.body)
		}
	}
	if len(post.body) != len(wantBody) || post.query != "username=ana%40empresa.pe" {
		t.Fatalf("alta: %+v", post)
	}
	if err := c.DeleteAppPassword(ctx, "ana@empresa.pe", apID); err != nil {
		t.Fatal(err)
	}
}

func TestContrasenasDeAplicacionFormasAlternativas(t *testing.T) {
	row := `{"id":"` + apID + `","name":"Movil","imap_access":true,"active":true,"created_at":"2026-09-01T00:00:00Z"}`
	c, _ := fakeDirectory(t, map[string]reply{
		"GET " + appPasswordsPath:  {200, `{"data":{"items":[` + row + `],"max":25}}`},
		"POST " + appPasswordsPath: {201, `{"data":{"id":"` + apID + `","name":"Movil","imap_access":true,"password":"clave"}}`},
	})
	list, err := c.AppPasswords(context.Background(), "ana@empresa.pe")
	if err != nil || list.Max != 25 || len(list.Items) != 1 {
		t.Fatalf("lista con tope: %+v %v", list, err)
	}
	created, err := c.CreateAppPassword(context.Background(), "ana@empresa.pe", domain.AppPasswordInput{Name: "Movil", Access: domain.AppPasswordAccess{IMAP: true}})
	if err != nil || created.Password != "clave" || created.ID != apID {
		t.Fatalf("alta plana: %+v %v", created, err)
	}
}

func TestRechazosDeLasContrasenasDeAplicacion(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   error
	}{
		{409, `{"error":{"code":"CONFLICT","message":"máximo"}}`, domain.ErrAppPasswordLimit},
		{201, `{"data":{"app_password":{"id":"no-uuid"},"password":"x"}}`, domain.ErrUnavailable},
		{201, `{"data":{"app_password":{"id":"` + apID + `"},"password":""}}`, domain.ErrUnavailable},
	} {
		client, _ := fakeDirectory(t, map[string]reply{"POST " + appPasswordsPath: {c.status, c.body}})
		if _, err := client.CreateAppPassword(context.Background(), "ana@empresa.pe", domain.AppPasswordInput{Name: "x"}); !errors.Is(err, c.want) {
			t.Fatalf("%d %s: %v", c.status, c.body, err)
		}
	}
	client, _ := fakeDirectory(t, map[string]reply{"DELETE " + appPasswordsPath + "/" + apID: {404, `{"error":{"code":"NOT_FOUND","message":"no"}}`}})
	if err := client.DeleteAppPassword(context.Background(), "ana@empresa.pe", apID); !errors.Is(err, domain.ErrAppPasswordNotFound) {
		t.Fatalf("%v", err)
	}
	client, _ = fakeDirectory(t, map[string]reply{"GET " + appPasswordsPath: {200, `{"data":[{"id":"x"}]}`}})
	if _, err := client.AppPasswords(context.Background(), "ana@empresa.pe"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("id invalido: %v", err)
	}
}

func TestReglasConReautenticacionYRechazosDelReenvioExterno(t *testing.T) {
	ok := `{"data":{"rules":[],"forwarding":{"enabled":true,"addresses":["fuera@otra.pe"],"keep_copy":true},"limits":{}}}`
	c, calls := fakeDirectory(t, map[string]reply{"PUT " + filtersPath: {200, ok}})
	in := domain.MailFiltersInput{Forwarding: domain.Forwarding{Enabled: true, Addresses: []string{"fuera@otra.pe"}, KeepCopy: true}}
	if _, err := c.SetFilters(context.Background(), "ana@empresa.pe", in); err != nil {
		t.Fatal(err)
	}
	if _, present := (*calls)[0].body["reauthenticated"]; present {
		t.Fatalf("sin reautenticar no se declara: %v", (*calls)[0].body)
	}
	in.Reauthenticated = true
	if _, err := c.SetFilters(context.Background(), "ana@empresa.pe", in); err != nil {
		t.Fatal(err)
	}
	if (*calls)[1].body["reauthenticated"] != true {
		t.Fatalf("reautenticada: %v", (*calls)[1].body)
	}

	for _, body := range []string{
		`{"error":{"code":"REAUTH_REQUIRED","message":"x","details":{"addresses":["fuera@otra.pe","b@c.pe"]}}}`,
		`{"error":{"code":"REAUTH_REQUIRED","message":"x","details":{"addresses":"fuera@otra.pe, b@c.pe"}}}`,
	} {
		client, _ := fakeDirectory(t, map[string]reply{"PUT " + filtersPath: {403, body}})
		_, err := client.SetFilters(context.Background(), "ana@empresa.pe", in)
		var reauth *domain.ReauthRequiredError
		if !errors.As(err, &reauth) || strings.Join(reauth.Addresses, ",") != "fuera@otra.pe,b@c.pe" {
			t.Fatalf("%s: %v", body, err)
		}
	}
	client, _ := fakeDirectory(t, map[string]reply{"PUT " + filtersPath: {422, `{"error":{"code":"EXTERNAL_FORWARDING_DISABLED","message":"prohibido","details":{"addresses":["fuera@otra.pe"]}}}`}})
	_, err := client.SetFilters(context.Background(), "ana@empresa.pe", in)
	var forbidden *domain.ExternalForwardingDisabledError
	if !errors.As(err, &forbidden) || len(forbidden.Addresses) != 1 {
		t.Fatalf("%v", err)
	}
	// Un 403 que no es de reautenticacion sigue siendo indisponibilidad, y un 422 corriente, validacion.
	client, _ = fakeDirectory(t, map[string]reply{"PUT " + filtersPath: {403, `{"error":{"code":"FORBIDDEN","message":"x"}}`}})
	if _, err := client.SetFilters(context.Background(), "ana@empresa.pe", in); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	client, _ = fakeDirectory(t, map[string]reply{"PUT " + filtersPath: {422, `{"error":{"code":"VALIDATION_ERROR","message":"mala","details":{"field":"forwarding.addresses[0]","max":5}}}`}})
	var verr *domain.ValidationError
	if _, err := client.SetFilters(context.Background(), "ana@empresa.pe", in); !errors.As(err, &verr) || verr.Field != "forwarding.addresses[0]" {
		t.Fatalf("un detalle numerico no rompe el envelope: %v", err)
	}
}
