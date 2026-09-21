package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const (
	testJobID    = "0a1b2c3d-0000-4000-8000-000000000001"
	testTenantID = "0a1b2c3d-0000-4000-8000-000000000002"
	testLeaseID  = "0a1b2c3d-0000-4000-8000-000000000003"
)

func claimBody() string {
	return `{"data":{"job_id":"` + testJobID + `","tenant_id":"` + testTenantID + `","lease_id":"` + testLeaseID + `","attempt":2,
	"source":{"host":"imap.origen.example","port":993,"tls":"ssl","username":"ana@origen.example","password":"s3creta-de-origen"},
	"destination":{"username":"ana@empresa.example"},"lease_seconds":90}}`
}

type recorded struct {
	Path   string
	Auth   string
	Body   map[string]any
	Method string
}

// apiServer graba cada peticion y responde lo que decide handler para la ruta.
func apiServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*API, *[]recorded) {
	t.Helper()
	var mu sync.Mutex
	var seen []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := recorded{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Method: r.Method}
		_ = json.Unmarshal(raw, &rec.Body)
		mu.Lock()
		seen = append(seen, rec)
		mu.Unlock()
		handler(w, r, raw)
	}))
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL)
	return NewAPI(base, testKey, "runner-1"), &seen
}

func TestAPIClaimConTrabajo(t *testing.T) {
	api, seen := apiServer(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(claimBody()))
	})
	job, err := api.Claim(context.Background())
	if err != nil || job == nil {
		t.Fatalf("job %v, error %v", job, err)
	}
	if job.JobID != testJobID || job.Attempt != 2 || job.Source.Password != "s3creta-de-origen" || job.Destination.Username != "ana@empresa.example" || job.LeaseSeconds != 90 {
		t.Fatalf("trabajo: %+v", job)
	}
	rec := (*seen)[0]
	if rec.Path != "/v1/claim" || rec.Method != http.MethodPost || rec.Auth != "Bearer "+testKey || rec.Body["runner_id"] != "runner-1" {
		t.Fatalf("peticion: %+v", rec)
	}
}

func TestAPIClaimSinTrabajo(t *testing.T) {
	api, _ := apiServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { w.WriteHeader(http.StatusNoContent) })
	job, err := api.Claim(context.Background())
	if job != nil || err != nil {
		t.Fatalf("job %v, error %v", job, err)
	}
}

func TestAPIClaimRechazaIdentificadoresInvalidos(t *testing.T) {
	api, _ := apiServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		_, _ = w.Write([]byte(strings.Replace(claimBody(), testJobID, "../../admin", 1)))
	})
	if _, err := api.Claim(context.Background()); err == nil {
		t.Fatal("un identificador con barras no debe llegar a una ruta")
	}
}

func TestAPIErroresDelSobre(t *testing.T) {
	api, _ := apiServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"LEASE_LOST","message":"contiene s3creta-de-origen"}}`))
	})
	job := &ClaimedJob{JobID: testJobID, TenantID: testTenantID, LeaseID: testLeaseID}
	_, err := api.Heartbeat(context.Background(), job, phaseInitial, Progress{})
	if !isLeaseLost(err) {
		t.Fatalf("error %v", err)
	}
	if strings.Contains(err.Error(), "s3creta") {
		t.Fatalf("el mensaje del servicio no debe propagarse: %v", err)
	}
	if retryable(err) {
		t.Fatal("LEASE_LOST no se reintenta")
	}
}

func TestAPICodigoDeErrorSaneado(t *testing.T) {
	api, _ := apiServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":{"code":"con espacios y \u001b[31mcolor"}}`))
	})
	err := api.Complete(context.Background(), &ClaimedJob{JobID: testJobID, TenantID: testTenantID, LeaseID: testLeaseID}, outcomeFailed, Progress{}, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "UNKNOWN" || ae.Status != 422 {
		t.Fatalf("error %v", err)
	}
	if retryable(err) {
		t.Fatal("un 422 no se reintenta")
	}
}

func TestAPIHeartbeatYCompleteLlevanElContrato(t *testing.T) {
	api, seen := apiServer(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			_, _ = w.Write([]byte(`{"data":{"cancel":true,"lease_seconds":120}}`))
			return
		}
		w.WriteHeader(200)
	})
	job := &ClaimedJob{JobID: testJobID, TenantID: testTenantID, LeaseID: testLeaseID}
	long := strings.Repeat("ñ", 400)
	folders := make([]FolderProgress, 600)
	for i := range folders {
		folders[i] = FolderProgress{Name: long, MessagesCopied: i}
	}
	reply, err := api.Heartbeat(context.Background(), job, phaseCatchup, Progress{MessagesCopied: 3, Folders: folders})
	if err != nil || !reply.Cancel || reply.LeaseSeconds != 120 {
		t.Fatalf("respuesta %+v, error %v", reply, err)
	}
	if err := api.Complete(context.Background(), job, outcomeFailed, Progress{}, &JobError{Code: codeVirusFound, Message: "1 mensaje con virus no se copio."}); err != nil {
		t.Fatal(err)
	}

	hb := (*seen)[0]
	if hb.Path != "/v1/tenants/"+testTenantID+"/jobs/"+testJobID+"/heartbeat" || hb.Body["lease_id"] != testLeaseID || hb.Body["phase"] != "catchup" {
		t.Fatalf("latido: %+v", hb)
	}
	gotFolders := hb.Body["progress"].(map[string]any)["folders"].([]any)
	if len(gotFolders) != maxFolders {
		t.Fatalf("carpetas enviadas %d, el servicio admite %d", len(gotFolders), maxFolders)
	}
	if name := gotFolders[0].(map[string]any)["name"].(string); len([]rune(name)) != maxFolderName {
		t.Fatalf("nombre de %d caracteres, el servicio admite %d", len([]rune(name)), maxFolderName)
	}
	done := (*seen)[1]
	if done.Path != "/v1/tenants/"+testTenantID+"/jobs/"+testJobID+"/complete" || done.Body["outcome"] != "failed" {
		t.Fatalf("cierre: %+v", done)
	}
	if e := done.Body["error"].(map[string]any); e["code"] != "virus_found" {
		t.Fatalf("error del cierre: %v", e)
	}
	if list := done.Body["progress"].(map[string]any)["folders"]; list == nil {
		t.Fatal("folders debe ser una lista vacia y no null")
	}
}

func TestAPIRespuestaDemasiadoGrande(t *testing.T) {
	api, _ := apiServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		_, _ = w.Write([]byte(`{"data":"` + strings.Repeat("a", maxResponseBytes+10) + `"}`))
	})
	if _, err := api.Claim(context.Background()); err == nil || !strings.Contains(err.Error(), "demasiado grande") {
		t.Fatalf("error %v", err)
	}
}

func TestAPINoSigueRedireccionesNiEnviaLaClaveAOtroHost(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked = true
		}
	}))
	defer other.Close()
	api, _ := apiServer(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		http.Redirect(w, r, other.URL+"/robo", http.StatusTemporaryRedirect)
	})
	if _, err := api.Claim(context.Background()); err == nil {
		t.Fatal("una redireccion debe ser un error")
	}
	if leaked {
		t.Fatal("la clave llego a otro host")
	}
}

func TestAPIPrefijoDeRuta(t *testing.T) {
	api, seen := apiServer(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { w.WriteHeader(http.StatusNoContent) })
	api.base.Path = "/interno"
	if _, err := api.Claim(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].Path != "/interno/v1/claim" {
		t.Fatalf("ruta %s", (*seen)[0].Path)
	}
}

func TestValidacionDelTrabajo(t *testing.T) {
	good := func() ClaimedJob {
		var j ClaimedJob
		if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSuffix(claimBody(), "}"), `{"data":`)), &j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	base := good()
	if err := base.validateIdentity(); err != nil {
		t.Fatal(err)
	}
	if err := base.validate(); err != nil {
		t.Fatalf("el trabajo de ejemplo debe ser valido: %v", err)
	}
	mutations := map[string]func(*ClaimedJob){
		"puerto 0":                func(j *ClaimedJob) { j.Source.Port = 0 },
		"puerto 70000":            func(j *ClaimedJob) { j.Source.Port = 70000 },
		"tls desconocido":         func(j *ClaimedJob) { j.Source.TLS = "auto" },
		"sin usuario":             func(j *ClaimedJob) { j.Source.Username = "" },
		"sin contrasena":          func(j *ClaimedJob) { j.Source.Password = "" },
		"contrasena con salto":    func(j *ClaimedJob) { j.Source.Password = "a\nb" },
		"usuario con control":     func(j *ClaimedJob) { j.Source.Username = "a\x00b" },
		"destino con separador":   func(j *ClaimedJob) { j.Destination.Username = "otro@empresa.example*maestro" },
		"destino con comillas":    func(j *ClaimedJob) { j.Destination.Username = `a"b@empresa.example` },
		"destino sin arroba":      func(j *ClaimedJob) { j.Destination.Username = "ana" },
		"destino con espacio":     func(j *ClaimedJob) { j.Destination.Username = "a b@empresa.example" },
		"destino con dos arrobas": func(j *ClaimedJob) { j.Destination.Username = "a@b@empresa.example" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			j := good()
			mutate(&j)
			if err := j.validate(); err == nil {
				t.Fatal("debe rechazarse")
			}
		})
	}
	j := good()
	j.JobID = "a/b"
	if err := j.validateIdentity(); err == nil {
		t.Fatal("identificador con barra")
	}
	j = good()
	j.LeaseSeconds = 1
	if err := j.validateIdentity(); err != nil || j.LeaseSeconds != 90 {
		t.Fatalf("un arrendamiento absurdo se sustituye por el defecto: %d, %v", j.LeaseSeconds, err)
	}
}
