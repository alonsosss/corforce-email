// Package cli es el subcomando `audit verificar-ancla` (ops/security/verificar-ancla.sh): comprueba
// FUERA del servidor un informe de anclas recibido por correo. Primero la firma, con la llave de
// la cadena que el operador guarda en su respaldo de secretos; despues, si se le da la base de la
// empresa, coteja cada ancla con la cadena actual. Es el uso del ancla externa (docs/adr/0006,
// seccion 8): un correo enviado no se puede borrar desde el servidor, asi que una cabeza que hoy
// va por detras de la del correo, o un hash distinto en esa posicion, es evidencia de manipulacion.
package cli

import (
	"bytes"
	"context"
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"os"
	"strings"
	"time"

	"github.com/alonsosss/corforce-email/pkg/crypto"
	"github.com/alonsosss/corforce-email/services/audit/internal/domain"
	"github.com/google/uuid"
)

// VerifyCommand es el primer argumento que hace que el binario de audit verifique en vez de servir.
const VerifyCommand = "verificar-ancla"

// Codigos de salida: 0 el informe es autentico y la cadena lo contiene; 1 evidencia de
// manipulacion (firma invalida, o la cadena ya no contiene un ancla); 2 no se pudo comprobar
// (argumentos, fichero, llave o base).
const (
	ExitOK       = 0
	ExitEvidence = 1
	ExitError    = 2
)

const dbTimeout = 30 * time.Second

// ChainFactsReader es lo que el cotejo necesita de la base de la empresa.
type ChainFactsReader interface {
	Facts(ctx context.Context, a domain.ChainAnchor) (domain.ChainFacts, error)
}

// OpenChain abre la base de la empresa que se da en --dsn y presta la lectura de la cadena; la
// aporta main, que es quien conoce el adaptador de Postgres.
type OpenChain func(ctx context.Context, dsn string) (reader ChainFactsReader, closeDB func(), err error)

// VerifyAnchors ejecuta el subcomando con args (sin el nombre del subcomando).
func VerifyAnchors(args []string, stdout, stderr io.Writer, openDB OpenChain) int {
	fs := flag.NewFlagSet(VerifyCommand, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dsn := fs.String("dsn", "", "DSN de la base de la empresa (mail_tenant_<slug>) para cotejar las anclas con la cadena actual")
	tenant := fs.String("tenant", "", "empresa a cotejar, por id o slug; sobra si el informe lleva una sola")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "uso: audit %s <correo.eml | texto del informe> [--dsn DSN [--tenant id|slug]]\n", VerifyCommand)
		fs.PrintDefaults()
	}
	// flag se detiene en el primer argumento suelto; el fichero va delante de las opciones en el
	// uso documentado, asi que se vuelve a parsear lo que queda tras cada uno.
	var files []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return ExitError
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(files) != 1 {
		fs.Usage()
		return ExitError
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		fmt.Fprintf(stderr, "verificar-ancla: %v\n", err)
		return ExitError
	}
	report, err := domain.ParseAnchorReport(extractText(raw))
	if err != nil {
		fmt.Fprintf(stderr, "verificar-ancla: el fichero no contiene un informe legible: %v\n", err)
		return ExitError
	}
	fmt.Fprintf(stdout, "Informe generado el %s (%s), %d anclas\n", report.GeneratedAt.UTC().Format(time.RFC3339), report.Cause, len(report.Anchors))
	if report.Broken != nil {
		fmt.Fprintf(stdout, "El informe se envio por una rotura: empresa %s, cadena %s, motivo %s\n", report.Broken.TenantID, report.Broken.Chain, report.Broken.Reason)
	}

	code := checkSignature(report, stdout, stderr)
	if code == ExitError {
		return code
	}
	if *dsn == "" {
		if code == ExitOK {
			fmt.Fprintln(stdout, "Sin --dsn no se coteja con la cadena actual.")
		}
		return code
	}
	if openDB == nil {
		fmt.Fprintln(stderr, "verificar-ancla: este binario no sabe abrir una base")
		return ExitError
	}
	dbCode := compareWithChain(report, *dsn, *tenant, openDB, stdout, stderr)
	if dbCode > code {
		return dbCode
	}
	return code
}

// checkSignature decide con la llave del entorno. Un informe sin firma cuando el operador tiene
// llave no es de fiar: un correo fabricado diria exactamente eso.
func checkSignature(report *domain.ParsedAnchorReport, stdout, stderr io.Writer) int {
	ring, err := crypto.LoadMACKeyRing("AUDIT_HASH_KEY", "AUDIT_HASH_KEYS_OLD")
	if err != nil {
		fmt.Fprintf(stderr, "verificar-ancla: %v\n", err)
		return ExitError
	}
	switch {
	case ring == nil && report.Signature == nil:
		fmt.Fprintln(stdout, "AVISO: el informe no lleva firma y no hay AUDIT_HASH_KEY: su autenticidad no se puede comprobar.")
		return ExitOK
	case ring == nil:
		fmt.Fprintf(stderr, "verificar-ancla: el informe va firmado (key_id %s) y falta AUDIT_HASH_KEY para comprobarlo\n", report.Signature.KeyID)
		return ExitError
	case report.Signature == nil:
		fmt.Fprintln(stdout, "FIRMA AUSENTE: el informe dice no llevar firma, pero la cadena tiene llave; no es de fiar.")
		return ExitEvidence
	}
	expected, err := ring.SignWith(report.Signature.KeyID, report.Block)
	if errors.Is(err, crypto.ErrUnknownKeyID) {
		fmt.Fprintf(stderr, "verificar-ancla: el informe se firmo con la llave %s, que no esta en AUDIT_HASH_KEY ni en AUDIT_HASH_KEYS_OLD\n", report.Signature.KeyID)
		return ExitError
	}
	if err != nil {
		fmt.Fprintf(stderr, "verificar-ancla: %v\n", err)
		return ExitError
	}
	if !hmac.Equal(expected, report.Signature.MAC) {
		fmt.Fprintln(stdout, "FIRMA INVALIDA: el bloque de anclas no es el que firmo audit; el correo se altero o se fabrico.")
		return ExitEvidence
	}
	fmt.Fprintf(stdout, "Firma correcta (key_id %s).\n", report.Signature.KeyID)
	return ExitOK
}

func compareWithChain(report *domain.ParsedAnchorReport, dsn, tenant string, openDB OpenChain, stdout, stderr io.Writer) int {
	anchors, err := anchorsOf(report, tenant)
	if err != nil {
		fmt.Fprintf(stderr, "verificar-ancla: %v\n", err)
		return ExitError
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	reader, closeDB, err := openDB(ctx, dsn)
	if err != nil {
		fmt.Fprintf(stderr, "verificar-ancla: base de la empresa: %v\n", err)
		return ExitError
	}
	defer closeDB()
	code := ExitOK
	for _, a := range anchors {
		facts, err := reader.Facts(ctx, a.ChainAnchor)
		if err != nil {
			fmt.Fprintf(stderr, "verificar-ancla: leer la cadena %s: %v\n", a.Chain, err)
			return ExitError
		}
		reason, warning := domain.CompareAnchor(a.ChainAnchor, facts)
		if warning != "" {
			fmt.Fprintf(stdout, "AVISO %s seq=%d: %s\n", a.Chain, a.HeadSeq, warning)
		}
		switch reason {
		case "":
			fmt.Fprintf(stdout, "OK %s: la fila %d sigue con el hash del correo (cabeza actual %d)\n", a.Chain, a.HeadSeq, facts.HeadSeq)
		case domain.ReasonHeadBehindAnchor:
			fmt.Fprintf(stdout, "MANIPULACION %s (%s): la cabeza actual es %d y el correo anclo la %d: se borraron las ultimas filas\n", a.Chain, reason, facts.HeadSeq, a.HeadSeq)
			code = ExitEvidence
		default:
			fmt.Fprintf(stdout, "MANIPULACION %s (%s): la fila %d ya no tiene el hash del correo: se reescribio o se borro\n", a.Chain, reason, a.HeadSeq)
			code = ExitEvidence
		}
	}
	return code
}

// anchorsOf elige las anclas de la empresa que se coteja: la que se pide (por id o slug) o la
// unica del informe.
func anchorsOf(report *domain.ParsedAnchorReport, tenant string) ([]domain.ReportedAnchor, error) {
	tenants := map[uuid.UUID]string{}
	var order []uuid.UUID
	for _, a := range report.Anchors {
		if _, seen := tenants[a.Tenant.ID]; !seen {
			order = append(order, a.Tenant.ID)
		}
		tenants[a.Tenant.ID] = a.Tenant.Slug
	}
	var want uuid.UUID
	switch {
	case tenant == "" && len(order) == 1:
		want = order[0]
	case tenant == "":
		var names []string
		for _, id := range order {
			names = append(names, fmt.Sprintf("%s (%s)", id, tenants[id]))
		}
		return nil, fmt.Errorf("el informe lleva %d empresas; indique cual cotejar con --tenant: %s", len(order), strings.Join(names, ", "))
	default:
		for _, id := range order {
			if id.String() == strings.ToLower(tenant) || (tenants[id] != "" && tenants[id] == tenant) {
				want = id
			}
		}
		if want == uuid.Nil {
			return nil, fmt.Errorf("el informe no lleva ninguna ancla de la empresa %q", tenant)
		}
	}
	var out []domain.ReportedAnchor
	for _, a := range report.Anchors {
		if a.Tenant.ID == want {
			out = append(out, a)
		}
	}
	return out, nil
}

// extractText devuelve el texto del informe: el cuerpo text/plain decodificado si el fichero es un
// correo completo (.eml, con sus cabeceras), o el propio fichero si es el texto pegado.
func extractText(raw []byte) string {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil || !looksLikeMail(msg.Header) {
		return string(raw)
	}
	text, ok := textPart(textproto.MIMEHeader(msg.Header), msg.Body)
	if !ok {
		return string(raw)
	}
	return text
}

func looksLikeMail(h mail.Header) bool {
	for _, k := range []string{"Content-Type", "From", "Message-Id", "Received"} {
		if h.Get(k) != "" {
			return true
		}
	}
	return false
}

// textPart busca la primera parte text/plain, bajando por los multipart, y la decodifica segun su
// Content-Transfer-Encoding (quoted-printable y base64 son lo que envia SES).
func textPart(h textproto.MIMEHeader, body io.Reader) (string, bool) {
	mediaType, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				return "", false
			}
			if text, ok := textPart(part.Header, part); ok {
				return text, true
			}
		}
	}
	if mediaType != "text/plain" {
		return "", false
	}
	var r io.Reader = body
	switch strings.ToLower(strings.TrimSpace(h.Get("Content-Transfer-Encoding"))) {
	case "quoted-printable":
		r = quotedprintable.NewReader(body)
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, body)
	}
	text, err := io.ReadAll(r)
	if err != nil {
		return "", false
	}
	return string(text), true
}
