package domain

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// El informe de anclas es la copia EXTERNA de la cabeza de cada cadena (docs/adr/0006, seccion 8):
// un correo en texto plano que sale del servidor y que quien lo recibe guarda fuera de el. Lleva
// solo identificadores de empresa, posiciones y hashes; nunca contenido de apuntes ni datos
// personales. El bloque entre los dos marcadores es lo que se firma y lo que el verificador
// (audit verificar-ancla) vuelve a leer, asi que su forma es un contrato: una linea por dato,
// claves fijas y sin nada que un cliente de correo tenga que interpretar.

// ReportCause dice por que salio un informe.
type ReportCause string

const (
	// ReportCauseScheduled es el informe periodico con la ultima ancla de cada cadena.
	ReportCauseScheduled ReportCause = "scheduled"
	// ReportCauseChainBroken es el envio inmediato de la empresa cuya cadena una verificacion dio por rota.
	ReportCauseChainBroken ReportCause = "chain_broken"
)

// Resultados de un envio, como etiquetas cerradas de la metrica audit_anchor_reports_total.
const (
	ReportResultSent       = "sent"
	ReportResultSuppressed = "suppressed"
	ReportResultRejected   = "rejected"
	ReportResultFailed     = "failed"
)

// AnchorReportFormat es la version del bloque firmado; sube si cambia una linea.
const AnchorReportFormat = 1

const (
	reportBegin         = "-----BEGIN CORE FORCE MAIL AUDIT ANCHORS-----"
	reportEnd           = "-----END CORE FORCE MAIL AUDIT ANCHORS-----"
	reportSubjectPrefix = "[Core Force Mail] Anclas de auditoria"
	reportSignatureNone = "signature: none"
	reportSignatureAlgo = "hmac-sha256"
	reportDateLayout    = "2006-01-02"
)

// TenantRef identifica una empresa en el informe: el id es la clave y el slug, la ayuda para
// quien lo lee.
type TenantRef struct {
	ID   uuid.UUID
	Slug string
}

// TenantAnchors es la ultima ancla de cada cadena de una empresa (como mucho una por cadena).
type TenantAnchors struct {
	Tenant  TenantRef
	Anchors []ChainAnchor
}

// ChainBreak es la rotura que motiva un envio inmediato.
type ChainBreak struct {
	TenantID uuid.UUID
	Chain    ChainName
	Reason   string
}

// AnchorReport es un informe antes de renderizarse.
type AnchorReport struct {
	GeneratedAt time.Time
	Cause       ReportCause
	Broken      *ChainBreak
	Tenants     []TenantAnchors
}

// ReportSignature es la firma del bloque: con que llave y el HMAC.
type ReportSignature struct {
	KeyID string
	MAC   []byte
}

// Subject es fijo y predecible: quien archiva los correos filtra por el.
func (r AnchorReport) Subject() string {
	s := reportSubjectPrefix + " " + r.GeneratedAt.UTC().Format(reportDateLayout)
	if r.Cause == ReportCauseChainBroken {
		s += ": cadena rota"
	}
	return s
}

// Block son los bytes que se firman: las lineas entre los marcadores, cada una terminada en
// salto de linea, con las empresas por id y las cadenas por nombre para que dos informes con los
// mismos datos den los mismos bytes.
func (r AnchorReport) Block() []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "format: %d\n", AnchorReportFormat)
	fmt.Fprintf(&b, "generated_at: %s\n", r.GeneratedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "cause: %s\n", r.Cause)
	if r.Broken != nil {
		fmt.Fprintf(&b, "broken: tenant=%s chain=%s reason=%s\n", r.Broken.TenantID, r.Broken.Chain, r.Broken.Reason)
	}
	tenants := append([]TenantAnchors(nil), r.Tenants...)
	sort.Slice(tenants, func(i, j int) bool { return tenants[i].Tenant.ID.String() < tenants[j].Tenant.ID.String() })
	for _, t := range tenants {
		anchors := append([]ChainAnchor(nil), t.Anchors...)
		sort.Slice(anchors, func(i, j int) bool { return anchors[i].Chain < anchors[j].Chain })
		for _, a := range anchors {
			fmt.Fprintf(&b, "anchor: tenant=%s slug=%s chain=%s seq=%d hash=%s hash_version=%d anchored_at=%s\n",
				t.Tenant.ID, reportSlug(t.Tenant.Slug), a.Chain, a.HeadSeq, a.HeadHash, a.HashVersion,
				a.AnchoredAt.UTC().Format(time.RFC3339))
		}
	}
	return []byte(b.String())
}

// reportSlug: el slug es una ayuda; uno vacio o con espacios (que romperia la linea) sale como "-".
func reportSlug(slug string) string {
	if slug == "" || strings.ContainsAny(slug, " \t\r\n") {
		return "-"
	}
	return slug
}

// Render es el cuerpo completo del correo en texto plano: una explicacion breve, el bloque
// firmado y la linea de firma. sig nil deja constancia de que el servicio no tiene llave.
func (r AnchorReport) Render(sig *ReportSignature) string {
	var b strings.Builder
	b.WriteString("Core Force Mail: anclas de las cadenas de auditoria\n")
	fmt.Fprintf(&b, "Fecha: %s\n", r.GeneratedAt.UTC().Format(time.RFC3339))
	switch {
	case r.Broken != nil:
		fmt.Fprintf(&b, "Motivo: una verificacion dio por rota la cadena %s de la empresa %s (%s)\n",
			r.Broken.Chain, r.Broken.TenantID, r.Broken.Reason)
	default:
		b.WriteString("Motivo: informe periodico\n")
	}
	b.WriteString("\n")
	b.WriteString("Este correo es la copia externa de la cabeza de cada cadena de hash de auditoria\n")
	b.WriteString("(docs/adr/0006). Guardelo fuera del servidor: si el servidor se ve comprometido,\n")
	b.WriteString("cotejar el ultimo correo recibido con la cadena actual (ops/security/verificar-ancla.sh)\n")
	b.WriteString("revela un borrado o una reescritura del rastro. Contiene solo identificadores de\n")
	b.WriteString("empresa, posiciones y hashes: ningun dato personal ni contenido de apuntes.\n")
	b.WriteString("\n")
	b.WriteString(reportBegin + "\n")
	b.Write(r.Block())
	b.WriteString(reportEnd + "\n")
	if sig == nil {
		b.WriteString(reportSignatureNone + "\n")
		b.WriteString("(audit no tiene AUDIT_HASH_KEY: el bloque no lleva firma y su autenticidad no se puede comprobar)\n")
	} else {
		fmt.Fprintf(&b, "signature: %s key_id=%s mac=%s\n", reportSignatureAlgo, sig.KeyID, hex.EncodeToString(sig.MAC))
	}
	return b.String()
}

// ReportedAnchor es un ancla leida de un informe, con la empresa a la que pertenece.
type ReportedAnchor struct {
	Tenant TenantRef
	ChainAnchor
}

// ParsedAnchorReport es lo que el verificador extrae de un correo: el bloque tal como llego
// (lo que hay que firmar de nuevo), sus datos y la firma que declara.
type ParsedAnchorReport struct {
	Block       []byte
	Format      int
	GeneratedAt time.Time
	Cause       ReportCause
	Broken      *ChainBreak
	Anchors     []ReportedAnchor
	// Signature es nil si el informe declara "signature: none".
	Signature *ReportSignature
}

var (
	ErrReportBlockMissing     = errors.New("el texto no contiene un bloque de anclas de Core Force Mail")
	ErrReportSignatureMissing = errors.New("el bloque no va seguido de una linea signature:")
)

// ParseAnchorReport lee un informe de su texto (el cuerpo del correo ya decodificado). Es
// estricto: un bloque repetido, una linea que no entiende o una firma mal formada son error,
// porque el verificador no puede dar por bueno lo que no sabe leer.
func ParseAnchorReport(text string) (*ParsedAnchorReport, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	begin, end := -1, -1
	for i, l := range lines {
		switch strings.TrimRight(l, " \t") {
		case reportBegin:
			if begin >= 0 {
				return nil, errors.New("el texto contiene mas de un bloque de anclas")
			}
			begin = i
		case reportEnd:
			if begin < 0 || end >= 0 {
				return nil, errors.New("marcador de fin sin marcador de inicio, o repetido")
			}
			end = i
		}
	}
	if begin < 0 || end < 0 {
		return nil, ErrReportBlockMissing
	}
	p := &ParsedAnchorReport{}
	var block strings.Builder
	for _, l := range lines[begin+1 : end] {
		l = strings.TrimRight(l, " \t")
		block.WriteString(l)
		block.WriteString("\n")
		if err := p.parseLine(l); err != nil {
			return nil, err
		}
	}
	p.Block = []byte(block.String())
	if p.Format != AnchorReportFormat {
		return nil, fmt.Errorf("formato del bloque %d; este verificador entiende el %d", p.Format, AnchorReportFormat)
	}
	if p.GeneratedAt.IsZero() || p.Cause == "" {
		return nil, errors.New("el bloque no lleva generated_at o cause")
	}
	for _, l := range lines[end+1:] {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, "signature:") {
			return nil, ErrReportSignatureMissing
		}
		return p, p.parseSignature(l)
	}
	return nil, ErrReportSignatureMissing
}

func (p *ParsedAnchorReport) parseLine(l string) error {
	key, value, ok := strings.Cut(l, ": ")
	if !ok {
		return fmt.Errorf("linea del bloque sin clave: %q", l)
	}
	var err error
	switch key {
	case "format":
		p.Format, err = strconv.Atoi(value)
	case "generated_at":
		p.GeneratedAt, err = time.Parse(time.RFC3339, value)
	case "cause":
		p.Cause = ReportCause(value)
	case "broken":
		p.Broken, err = parseBreak(value)
	case "anchor":
		var a ReportedAnchor
		if a, err = parseReportedAnchor(value); err == nil {
			p.Anchors = append(p.Anchors, a)
		}
	default:
		return fmt.Errorf("linea del bloque desconocida: %q", key)
	}
	if err != nil {
		return fmt.Errorf("linea %s: %w", key, err)
	}
	return nil
}

func (p *ParsedAnchorReport) parseSignature(l string) error {
	if l == reportSignatureNone {
		return nil
	}
	fields, err := keyValues(strings.TrimPrefix(l, "signature: "+reportSignatureAlgo+" "), "key_id", "mac")
	if err != nil {
		return fmt.Errorf("firma: %w", err)
	}
	mac, err := hex.DecodeString(fields["mac"])
	if err != nil || len(mac) != 32 {
		return errors.New("firma: el mac no es HMAC-SHA256 en hexadecimal")
	}
	if len(fields["key_id"]) != 16 {
		return errors.New("firma: key_id mal formado")
	}
	p.Signature = &ReportSignature{KeyID: fields["key_id"], MAC: mac}
	return nil
}

func parseBreak(v string) (*ChainBreak, error) {
	f, err := keyValues(v, "tenant", "chain", "reason")
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(f["tenant"])
	if err != nil {
		return nil, err
	}
	return &ChainBreak{TenantID: id, Chain: ChainName(f["chain"]), Reason: f["reason"]}, nil
}

func parseReportedAnchor(v string) (ReportedAnchor, error) {
	var a ReportedAnchor
	f, err := keyValues(v, "tenant", "slug", "chain", "seq", "hash", "hash_version", "anchored_at")
	if err != nil {
		return a, err
	}
	if a.Tenant.ID, err = uuid.Parse(f["tenant"]); err != nil {
		return a, err
	}
	if f["slug"] != "-" {
		a.Tenant.Slug = f["slug"]
	}
	a.TenantID = a.Tenant.ID
	a.Chain = ChainName(f["chain"])
	if a.HeadSeq, err = strconv.ParseInt(f["seq"], 10, 64); err != nil {
		return a, err
	}
	if a.HashVersion, err = strconv.Atoi(f["hash_version"]); err != nil {
		return a, err
	}
	if a.AnchoredAt, err = time.Parse(time.RFC3339, f["anchored_at"]); err != nil {
		return a, err
	}
	if _, err = hex.DecodeString(f["hash"]); err != nil || f["hash"] == "" {
		return a, errors.New("hash no hexadecimal")
	}
	a.HeadHash = f["hash"]
	return a, nil
}

// keyValues lee "k=v k=v" exigiendo exactamente las claves dadas, en cualquier orden.
func keyValues(s string, keys ...string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Split(bufio.ScanWords)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || v == "" {
			return nil, fmt.Errorf("campo mal formado: %q", sc.Text())
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("campo repetido: %s", k)
		}
		out[k] = v
	}
	for _, k := range keys {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("falta el campo %s", k)
		}
	}
	if len(out) != len(keys) {
		return nil, errors.New("campos de mas")
	}
	return out, nil
}

// ChainFacts es lo que el verificador lee de la cadena actual para cotejar un ancla del informe.
type ChainFacts struct {
	// HeadSeq es la posicion de la ultima fila firmada; 0 si la cadena esta vacia.
	HeadSeq int64
	// HashAtSeq es el hash de la fila que hoy ocupa la posicion del ancla; vacio si no hay fila.
	HashAtSeq string
	// AnchorRecorded dice si audit.chain_anchors conserva esa misma ancla.
	AnchorRecorded bool
}

// CompareAnchor coteja un ancla recibida por correo con la cadena actual y devuelve el codigo de
// rotura (los mismos que el verificador del servicio) o "" si la cadena la contiene, y un aviso si
// la tabla de anclas ya no la conserva: eso solo lo hace quien escribe en la base como dueno.
func CompareAnchor(a ChainAnchor, f ChainFacts) (reason string, warning string) {
	if !f.AnchorRecorded {
		warning = "audit.chain_anchors ya no conserva esta ancla: se borro desde la base"
	}
	switch {
	case f.HeadSeq < a.HeadSeq:
		return ReasonHeadBehindAnchor, warning
	case f.HashAtSeq != a.HeadHash:
		return ReasonAnchorMismatch, warning
	}
	return "", warning
}
