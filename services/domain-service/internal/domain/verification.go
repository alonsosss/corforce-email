package domain

import (
	"fmt"
	"strings"
	"time"
)

// Observation es lo que el DNS respondio para un registro. Err distingue "no se pudo
// consultar" (timeout, resolver caido) de "no hay registro": lo primero no dice nada
// sobre la zona del cliente y no debe cambiar el estado de un dominio verificado.
type Observation struct {
	TXT []string
	MX  []MXRecord
	Err error
}

// Outcome resume una verificacion completa.
type Outcome string

const (
	// OutcomeVerified: todos los registros obligatorios estan publicados.
	OutcomeVerified Outcome = "verified"
	// OutcomeFailed: falta o esta mal algun registro obligatorio, y el DNS respondio.
	OutcomeFailed Outcome = "failed"
	// OutcomeInconclusive: alguna consulta obligatoria no pudo hacerse; no se sabe.
	OutcomeInconclusive Outcome = "inconclusive"
)

// VerificationResult es lo que produce Evaluate: un check por registro esperado y el
// veredicto global.
type VerificationResult struct {
	Checks  []DNSCheck
	Outcome Outcome
	// SignWithPrevious indica que el TXT del selector nuevo aun no esta publicado pero
	// el del anterior sigue en pie: los motores deben seguir firmando con el anterior
	// hasta que el cliente publique el nuevo.
	SignWithPrevious bool
}

// Evaluate compara los registros esperados con lo observado. Es puro: recibe las
// respuestas ya consultadas para que la logica se pruebe sin red.
//
// Durante la gracia de una rotacion DKIM, que el selector nuevo aun no este publicado no
// bloquea si el anterior sigue publicado: el correo se firma con el anterior y sigue
// verificando. El check del nuevo queda en falso con la instruccion para el cliente.
func Evaluate(d *Domain, expected []DNSRecord, observed map[RecordKind]Observation, now time.Time) VerificationResult {
	result := VerificationResult{Outcome: OutcomeVerified, Checks: make([]DNSCheck, 0, len(expected))}
	transient := make(map[RecordKind]bool, len(expected))
	for _, rec := range expected {
		check := DNSCheck{
			TenantID: d.TenantID, DomainID: d.ID, CheckedAt: now,
			Record: rec.Record, Expected: rec.Value,
		}
		obs := observed[rec.Record]
		if obs.Err != nil {
			check.Detail = "no se pudo consultar el DNS: " + obs.Err.Error()
			transient[rec.Record] = true
		} else {
			check.OK, check.Observed, check.Detail = evaluateRecord(d, rec, obs)
		}
		result.Checks = append(result.Checks, check)
	}
	// El indice se construye con el slice ya completo: un puntero tomado durante los
	// append quedaria apuntando a una copia vieja.
	checks := make(map[RecordKind]*DNSCheck, len(result.Checks))
	for i := range result.Checks {
		checks[result.Checks[i].Record] = &result.Checks[i]
	}

	if cur, ok := checks[RecordDKIM]; ok && !cur.OK && !transient[RecordDKIM] {
		if prev, ok := checks[RecordDKIMPrevious]; ok && prev.OK {
			result.SignWithPrevious = true
			cur.Detail = "publique el TXT del selector nuevo; mientras tanto se firma con " + d.DKIMPreviousSelector
		}
	}

	for _, rec := range expected {
		check := checks[rec.Record]
		if check.OK || !rec.Required {
			continue
		}
		if rec.Record == RecordDKIM && result.SignWithPrevious {
			continue
		}
		if transient[rec.Record] {
			result.Outcome = OutcomeInconclusive
		} else if result.Outcome != OutcomeInconclusive {
			result.Outcome = OutcomeFailed
		}
	}
	return result
}

func evaluateRecord(d *Domain, rec DNSRecord, obs Observation) (ok bool, observed, detail string) {
	switch rec.Record {
	case RecordOwnershipTXT:
		return evaluateOwnership(rec.Value, obs.TXT)
	case RecordMX:
		return evaluateMX(rec.Value, obs.MX)
	case RecordSPF:
		return evaluateSPF(rec.Value, obs.TXT)
	case RecordDKIM:
		return evaluateDKIM(d.DKIMPublicKey, obs.TXT)
	case RecordDKIMPrevious:
		return evaluateDKIM(d.DKIMPreviousPublicKey, obs.TXT)
	case RecordDMARC:
		return evaluateDMARC(d.DMARCPolicy, obs.TXT)
	}
	return false, "", "registro desconocido"
}

func evaluateOwnership(expected string, txt []string) (bool, string, string) {
	if len(txt) == 0 {
		return false, "", "no hay registro TXT de propiedad"
	}
	for _, v := range txt {
		if strings.TrimSpace(v) == expected {
			return true, v, ""
		}
	}
	return false, strings.Join(txt, " | "), "el TXT de propiedad no contiene el token esperado"
}

func evaluateMX(expected string, mx []MXRecord) (bool, string, string) {
	if len(mx) == 0 {
		return false, "", "no hay registros MX"
	}
	// expected tiene la forma "<host> priority N"; solo el host decide.
	wantHost := normalizeHost(strings.Fields(expected)[0])
	seen := make([]string, 0, len(mx))
	for _, r := range mx {
		seen = append(seen, fmt.Sprintf("%s priority %d", normalizeHost(r.Host), r.Priority))
		if normalizeHost(r.Host) == wantHost {
			return true, strings.Join(seen, " | "), ""
		}
	}
	return false, strings.Join(seen, " | "), "ningun MX apunta a " + wantHost
}

// evaluateSPF exige un unico registro SPF (RFC 7208: mas de uno invalida la politica) que
// contenga el include de la plataforma como mecanismo completo.
func evaluateSPF(expected string, txt []string) (bool, string, string) {
	var spf []string
	for _, v := range txt {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "v=spf1") {
			spf = append(spf, strings.TrimSpace(v))
		}
	}
	if len(spf) == 0 {
		return false, "", "no hay registro SPF"
	}
	if len(spf) > 1 {
		return false, strings.Join(spf, " | "), "hay mas de un registro SPF; debe quedar uno solo"
	}
	include := strings.Fields(expected)[1]
	for _, mech := range strings.Fields(spf[0]) {
		if strings.EqualFold(mech, include) {
			return true, spf[0], ""
		}
	}
	return false, spf[0], "el SPF no incluye " + include
}

// evaluateDKIM lee las etiquetas del TXT y compara la clave publica sin espacios: hay
// paneles que parten el valor en varias cadenas y devuelven espacios entre ellas.
func evaluateDKIM(publicKey string, txt []string) (bool, string, string) {
	if len(txt) == 0 {
		return false, "", "no hay registro TXT de DKIM para el selector"
	}
	want := strings.Join(strings.Fields(publicKey), "")
	for _, v := range txt {
		tags := parseTags(v)
		if k, ok := tags["k"]; ok && !strings.EqualFold(k, "rsa") {
			return false, v, "el registro DKIM declara un tipo de clave distinto de rsa"
		}
		p, ok := tags["p"]
		if !ok {
			continue
		}
		if strings.Join(strings.Fields(p), "") == want {
			return true, v, ""
		}
		return false, v, "la clave publica del registro DKIM no coincide con la del selector"
	}
	return false, strings.Join(txt, " | "), "el registro DKIM no lleva la etiqueta p con la clave publica"
}

// evaluateDMARC acepta cualquier politica valida; si es mas laxa que la configurada lo
// dice en detail, pero el registro cuenta como publicado.
func evaluateDMARC(configured DMARCPolicy, txt []string) (bool, string, string) {
	for _, v := range txt {
		tags := parseTags(v)
		if !strings.EqualFold(tags["v"], "DMARC1") {
			continue
		}
		policy := DMARCPolicy(strings.ToLower(tags["p"]))
		if !policy.Valid() {
			return false, v, "el registro DMARC no declara una politica p valida"
		}
		if policy != configured {
			return true, v, fmt.Sprintf("la politica publicada es %s y la configurada %s", policy, configured)
		}
		return true, v, ""
	}
	return false, strings.Join(txt, " | "), "no hay registro DMARC (recomendado)"
}

// parseTags lee un registro tag=value; tag=value (DKIM y DMARC comparten el formato).
func parseTags(record string) map[string]string {
	tags := make(map[string]string)
	for _, part := range strings.Split(record, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		tags[strings.ToLower(strings.TrimSpace(kv[0]))] = strings.TrimSpace(kv[1])
	}
	return tags
}
