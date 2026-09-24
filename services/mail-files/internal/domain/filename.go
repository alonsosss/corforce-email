package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// MaxFileNameBytes acota el nombre guardado y servido; cabe en la columna file_name.
	MaxFileNameBytes = 200
	// maxExtensionBytes es la extension que se conserva al recortar un nombre largo.
	maxExtensionBytes = 16
)

// unsafeNameRunes son los caracteres que un sistema de ficheros o una cabecera HTTP interpretan.
const unsafeNameRunes = `<>:"/\|?*`

// windowsDevices son los nombres que Windows reserva en cualquier carpeta, con o sin extension.
var windowsDevices = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// SafeFileName convierte el nombre que da el navegador en uno que se puede guardar, mostrar y servir
// en Content-Disposition sin sorpresas: solo el ultimo segmento de una ruta, sin caracteres de
// control ni de formato (los que invierten el sentido del texto y hacen que "fdp.exe" se lea
// "exe.pdf"), sin los caracteres reservados de los sistemas de ficheros, sin espacios ni puntos en
// los extremos, sin nombres de dispositivo de Windows y con un tope de bytes que respeta la extension.
func SafeFileName(raw string) (string, error) {
	name := strings.ToValidUTF8(raw, "_")
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range name {
		switch {
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteRune(' ')
			}
			lastSpace = true
			continue
		case strings.ContainsRune(unsafeNameRunes, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
		lastSpace = false
	}
	name = strings.Trim(b.String(), " .")
	if name == "" {
		return "", NewValidationError("name", "el fichero no tiene un nombre válido")
	}
	stem, _, _ := strings.Cut(name, ".")
	if windowsDevices[strings.ToUpper(strings.TrimSpace(stem))] {
		name = "_" + name
	}
	return truncateName(name), nil
}

// truncateName recorta el nombre a MaxFileNameBytes sin partir un caracter y conservando una
// extension corta.
func truncateName(name string) string {
	if len(name) <= MaxFileNameBytes {
		return name
	}
	ext := ""
	if i := strings.LastIndexByte(name, '.'); i > 0 && len(name)-i <= maxExtensionBytes {
		name, ext = name[:i], name[i:]
	}
	limit := MaxFileNameBytes - len(ext)
	for len(name) > limit {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return strings.TrimRight(name, " .") + ext
}

// ASCIIFileName es la version ASCII del nombre para el parametro filename de Content-Disposition,
// que los clientes antiguos leen en lugar de filename*: todo lo que no es ASCII imprimible, y las
// comillas, la barra inversa y el porcentaje, pasan a guion bajo.
func ASCIIFileName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// RFC5987 codifica el nombre para el parametro filename* con el juego UTF-8: solo los attr-char de
// RFC 5987 viajan tal cual; el resto va en %XX.
func RFC5987(name string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if isAttrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

func isAttrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
