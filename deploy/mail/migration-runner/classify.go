package main

import (
	"fmt"
	"strings"
)

// Codigos de error de un trabajo (contrato con mail-migration).
const (
	codeSourceAuthFailed     = "source_auth_failed"
	codeSourceUnreachable    = "source_unreachable"
	codeSourceBlockedAddress = "source_blocked_address"
	codeSourceTLSFailed      = "source_tls_failed"
	codeDestinationFailed    = "destination_failed"
	codeQuotaExceeded        = "quota_exceeded"
	codeTimeout              = "timeout"
	codeVirusFound           = "virus_found"
	codeImapsyncFailed       = "imapsync_failed"
)

// Codigos de salida de imapsync que distinguen la causa (imapsync, tabla EXIT_*).
const (
	exitConnectionSource = 101
	exitConnectionDest   = 102
	exitTLS              = 12
	exitAuthSource       = 161
	exitAuthDest         = 162
	exitWithErrors       = 111
	exitOverQuota        = 113
	exitVirusAppend      = 119
	exitUsage            = 64
)

// verdict es el resultado de una pasada. Partial indica que imapsync llego al final del buzon con
// algunos mensajes sin copiar (un virus, un mensaje rechazado): la pasada de repaso todavia tiene
// sentido. Una causa que afecta a todo el trabajo (credenciales, conexion, cuota) corta.
type verdict struct {
	Err     *JobError
	Partial bool
}

func (v verdict) ok() bool { return v.Err == nil }

func fail(code, message string, partial bool) verdict {
	return verdict{Err: &JobError{Code: code, Message: message}, Partial: partial}
}

// classify traduce el codigo de salida de imapsync y lo que el analizador vio en una causa con
// nombre. Los mensajes son fijos y llevan solo contadores: nunca texto de la salida de imapsync.
func classify(exit int, f Findings) verdict {
	if exit == 0 {
		return classifyCompleted(f)
	}
	switch exit {
	case exitConnectionSource:
		if f.SourceTLS {
			return fail(codeSourceTLSFailed, "No se pudo verificar el certificado TLS del servidor de origen.", false)
		}
		return fail(codeSourceUnreachable, "No se pudo conectar con el servidor de origen.", false)
	case exitTLS:
		if f.DestTLS && !f.SourceTLS {
			return fail(codeDestinationFailed, "No se pudo establecer TLS con el servidor de destino.", false)
		}
		return fail(codeSourceTLSFailed, "No se pudo establecer TLS verificado con el servidor de origen.", false)
	case exitConnectionDest:
		return fail(codeDestinationFailed, "No se pudo conectar con el servidor de destino.", false)
	case exitAuthSource:
		return fail(codeSourceAuthFailed, "El servidor de origen rechazo el usuario o la contrasena.", false)
	case exitAuthDest:
		return fail(codeDestinationFailed, "El servidor de destino rechazo el acceso al buzon.", false)
	case exitOverQuota:
		return fail(codeQuotaExceeded, "El buzon de destino no tiene espacio para el resto de los mensajes.", false)
	case exitVirusAppend:
		return fail(codeVirusFound, "El servidor de destino rechazo mensajes por contener virus.", false)
	case exitWithErrors:
		v := classifyCompleted(f)
		if v.ok() {
			return fail(codeImapsyncFailed, "Algunos mensajes no se copiaron.", true)
		}
		if f.OverQuota && v.Err.Code == codeImapsyncFailed {
			return fail(codeQuotaExceeded, "El buzon de destino no tiene espacio para el resto de los mensajes.", false)
		}
		return v
	case exitUsage:
		if f.ScanUnavailable > 0 {
			return fail(codeImapsyncFailed, "El analisis antivirus no esta disponible.", false)
		}
	}
	return fail(codeImapsyncFailed, fmt.Sprintf("imapsync termino con el codigo %d.", exit), false)
}

// classifyCompleted juzga una pasada que imapsync cerro con exito. Que salga 0 no basta: cuando
// --pipemess rechaza un mensaje (virus, clamd caido) imapsync lo da por omitido y termina con 0.
func classifyCompleted(f Findings) verdict {
	var parts []string
	code := ""
	partial := true
	set := func(c, part string, stops bool) {
		if code == "" {
			code = c
		}
		if stops {
			partial = false
		}
		parts = append(parts, part)
	}
	if f.ScanInfected > 0 {
		set(codeVirusFound, plural(f.ScanInfected, "mensaje con virus no se copio", "mensajes con virus no se copiaron"), false)
	}
	if f.ScanUnavailable > 0 {
		set(codeImapsyncFailed, plural(f.ScanUnavailable, "mensaje no se pudo analizar con el antivirus", "mensajes no se pudieron analizar con el antivirus"), true)
	}
	if f.ScanTooBig > 0 {
		set(codeImapsyncFailed, plural(f.ScanTooBig, "mensaje supera el tamano analizable", "mensajes superan el tamano analizable"), false)
	}
	if code == "" && (f.NotInDest > 0 || f.DetectedErrors > 0) {
		set(codeImapsyncFailed, "Algunos mensajes no se copiaron", false)
	}
	if code == "" {
		return verdict{}
	}
	return fail(code, strings.Join(parts, "; ")+".", partial)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
