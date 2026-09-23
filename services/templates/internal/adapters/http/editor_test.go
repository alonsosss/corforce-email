package http

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alonsosss/corforce-email/pkg/authz"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/services/templates/internal/app"
	"github.com/alonsosss/corforce-email/services/templates/internal/app/apptest"
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/google/uuid"
)

type editorAPI struct {
	routes http.Handler
	uc     *app.UseCase
	tenant uuid.UUID
	user   uuid.UUID
}

func newEditorAPI(t *testing.T, withScanner bool) *editorAPI {
	t.Helper()
	deps := app.Deps{
		Repo: apptest.NewRepo(), Tx: &apptest.Tx{}, Renderer: &apptest.Renderer{}, Events: &apptest.Events{},
		BrandKits: apptest.NewBrandKits(), Assets: apptest.NewAssets(), Store: apptest.NewStore(),
	}
	if withScanner {
		deps.Scanner = &apptest.Scanner{}
	}
	uc := app.New(deps)
	return &editorAPI{
		routes: NewHandler(uc, authz.NewChecker(unreachableAccessControl, "test")).Routes(),
		uc:     uc, tenant: uuid.New(), user: uuid.New(),
	}
}

func (a *editorAPI) do(method, path, contentType string, body []byte, roles ...string) *httptest.ResponseRecorder {
	if len(roles) == 0 {
		roles = []string{middleware.RoleTenantAdmin}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	ctx := middleware.WithTenantID(req.Context(), a.tenant.String())
	ctx = context.WithValue(ctx, middleware.CtxUserID, a.user.String())
	ctx = context.WithValue(ctx, middleware.CtxRoles, roles)
	rec := httptest.NewRecorder()
	a.routes.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func multipartImage(t *testing.T, field string) (string, []byte) {
	t.Helper()
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewGray(image.Rect(0, 0, 8, 4))); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile(field, "portada.png")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(img.Bytes())
	mw.Close()
	return mw.FormDataContentType(), body.Bytes()
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("JSON invalido: %v: %s", err, rec.Body)
	}
	return env.Error.Code
}

func TestSubidaSinAntivirusResponde503(t *testing.T) {
	api := newEditorAPI(t, false)
	ct, body := multipartImage(t, "file")
	rec := api.do(http.MethodPost, "/assets", ct, body)
	if rec.Code != http.StatusServiceUnavailable || errorCode(t, rec) != "SCANNER_UNAVAILABLE" {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestSubidaYListadoDeImagenes(t *testing.T) {
	api := newEditorAPI(t, true)
	ct, body := multipartImage(t, "file")
	rec := api.do(http.MethodPost, "/assets", ct, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var created struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "url", "content_type", "size_bytes", "width", "height", "name", "created_at"} {
		if _, ok := created.Data[key]; !ok {
			t.Errorf("falta %q: %v", key, created.Data)
		}
	}
	if created.Data["content_type"] != "image/png" || created.Data["width"] != float64(8) {
		t.Fatalf("imagen: %v", created.Data)
	}
	if rec := api.do(http.MethodPost, "/assets", ct, body); rec.Code != http.StatusOK {
		t.Fatalf("la misma imagen devuelve la existente con 200: %d", rec.Code)
	}

	list := api.do(http.MethodGet, "/assets?limit=10", "", nil)
	var page struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			NextCursor *string          `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || len(page.Data.Items) != 1 || page.Data.NextCursor != nil {
		t.Fatalf("listado: %s", list.Body)
	}
	if rec := api.do(http.MethodDelete, "/assets/"+created.Data["id"].(string), "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("borrado: %d", rec.Code)
	}

	ct, body = multipartImage(t, "otro")
	if rec := api.do(http.MethodPost, "/assets", ct, body); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("sin campo file: %d", rec.Code)
	}
	if rec := api.do(http.MethodPost, "/assets", "application/json", []byte(`{}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("sin multipart: %d", rec.Code)
	}
	if rec := api.do(http.MethodGet, "/assets?limit=0", "", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit fuera de rango: %d", rec.Code)
	}
}

func TestPublicarMarketingConErroresResponde409ConLasIncidencias(t *testing.T) {
	api := newEditorAPI(t, true)
	tpl, _, err := api.uc.CreateTemplate(context.Background(), api.tenant, api.user, app.CreateTemplateInput{
		Name: "Promo", Kind: domain.KindMarketing, Content: domain.Content{Subject: "Promo", HTML: "<p>Hola</p>"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := api.do(http.MethodPost, "/"+tpl.ID.String()+"/versions/1/publish", "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var env struct {
		Error struct {
			Code   string `json:"code"`
			Issues []struct {
				Code, Severity string
			} `json:"issues"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.Code != "DELIVERABILITY_FAILED" || len(env.Error.Issues) < 2 {
		t.Fatalf("cuerpo: %s", rec.Body)
	}

	check := api.do(http.MethodPost, "/"+tpl.ID.String()+"/versions/1/check", "", nil)
	if check.Code != http.StatusOK || !strings.Contains(check.Body.String(), `"passed":false`) ||
		!strings.Contains(check.Body.String(), `"available":false`) {
		t.Fatalf("verificacion de la version: %d %s", check.Code, check.Body)
	}
}

func TestCheckYKitDeMarca(t *testing.T) {
	api := newEditorAPI(t, true)
	rec := api.do(http.MethodPost, "/check", "application/json",
		[]byte(`{"kind":"marketing","subject":"Hola","html":"<p>{{.first_name}}</p>","variables":{"first_name":"Ana"}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	for _, key := range []string{`"passed"`, `"issues"`, `"stats"`, `"spam"`, `"text_image_ratio"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("falta %s: %s", key, rec.Body)
		}
	}
	if rec := api.do(http.MethodPost, "/check", "application/json", []byte(`{"kind":"otro","subject":"x","html":"x"}`)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("tipo invalido: %d", rec.Code)
	}

	empty := api.do(http.MethodGet, "/brand-kit", "", nil)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"updated_at":null`) || !strings.Contains(empty.Body.String(), `"colors":[]`) {
		t.Fatalf("kit vacio: %s", empty.Body)
	}
	put := api.do(http.MethodPut, "/brand-kit", "application/json",
		[]byte(`{"logo_asset_id":null,"colors":["#0b5fff"],"fonts":["Inter"],"footer":{"company":"Acme SAC","address":"Av. Siempre Viva 742","website":"https://acme.pe","support_email":"hola@acme.pe"}}`))
	if put.Code != http.StatusOK || !strings.Contains(put.Body.String(), `"#0B5FFF"`) {
		t.Fatalf("kit guardado: %d %s", put.Code, put.Body)
	}
	if rec := api.do(http.MethodPut, "/brand-kit", "application/json", []byte(`{"fonts":["Papyrus"]}`)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("fuente invalida: %d", rec.Code)
	}
}

func TestLasRutasDelEditorExigenSuPermiso(t *testing.T) {
	api := newEditorAPI(t, true)
	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/brand-kit"}, {http.MethodPut, "/brand-kit"}, {http.MethodGet, "/assets"},
		{http.MethodPost, "/assets"}, {http.MethodDelete, "/assets/" + uuid.NewString()}, {http.MethodPost, "/check"},
	} {
		if rec := api.do(r.method, r.path, "", nil, "editor"); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s sin politica comprobable debe fallar cerrado: %d", r.method, r.path, rec.Code)
		}
	}
}
