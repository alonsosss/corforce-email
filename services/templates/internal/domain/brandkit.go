package domain

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Limites del kit de marca.
const (
	MaxBrandColors        = 12
	MaxBrandFonts         = 6
	MaxFooterCompany      = 200
	MaxFooterAddress      = 500
	MaxFooterWebsite      = 2048
	MaxFooterSupportEmail = 320
)

// BrandFont es una tipografia admitida en el kit con la pila CSS que la acompana: si el cliente
// de correo no la tiene (o no carga fuentes web), cae a la alternativa y el diseno no se rompe.
type BrandFont struct {
	Name  string `json:"name"`
	Stack string `json:"stack"`
	Web   bool   `json:"web"`
}

// brandFonts es la lista cerrada. Las del sistema estan en Windows, macOS, iOS y Android (o
// tienen un equivalente metrico que el cliente sustituye). Las web solo las cargan Apple Mail,
// iOS y algunos clientes de Android; se admiten las cinco mas extendidas porque todas tienen
// una sans-serif del sistema de metricas parecidas como alternativa, y Outlook y Gmail, que no
// cargan fuentes web, muestran esa alternativa sin descuadrar el diseno.
var brandFonts = []BrandFont{
	{Name: "Arial", Stack: "Arial, Helvetica, sans-serif"},
	{Name: "Helvetica", Stack: "Helvetica, Arial, sans-serif"},
	{Name: "Georgia", Stack: "Georgia, 'Times New Roman', Times, serif"},
	{Name: "Times New Roman", Stack: "'Times New Roman', Times, serif"},
	{Name: "Verdana", Stack: "Verdana, Geneva, sans-serif"},
	{Name: "Tahoma", Stack: "Tahoma, Verdana, Segoe, sans-serif"},
	{Name: "Trebuchet MS", Stack: "'Trebuchet MS', Helvetica, sans-serif"},
	{Name: "Courier New", Stack: "'Courier New', Courier, monospace"},
	{Name: "Inter", Stack: "Inter, Arial, Helvetica, sans-serif", Web: true},
	{Name: "Roboto", Stack: "Roboto, Arial, Helvetica, sans-serif", Web: true},
	{Name: "Open Sans", Stack: "'Open Sans', Arial, Helvetica, sans-serif", Web: true},
	{Name: "Lato", Stack: "Lato, Arial, Helvetica, sans-serif", Web: true},
	{Name: "Montserrat", Stack: "Montserrat, Arial, Helvetica, sans-serif", Web: true},
}

// BrandFonts devuelve la lista cerrada de tipografias, en orden estable.
func BrandFonts() []BrandFont { return append([]BrandFont(nil), brandFonts...) }

func isBrandFont(name string) bool {
	for _, f := range brandFonts {
		if f.Name == name {
			return true
		}
	}
	return false
}

var brandColorRegex = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// BrandFooter son los datos del pie legal. Address es la direccion fisica que exige el
// verificador en marketing (regla missing_physical_address).
type BrandFooter struct {
	Company      string
	Address      string
	Website      string
	SupportEmail string
}

// BrandKit es el kit de marca de una empresa. UpdatedAt nil significa que la empresa aun no lo
// ha guardado nunca.
type BrandKit struct {
	TenantID    uuid.UUID
	LogoAssetID *uuid.UUID
	Colors      []string
	Fonts       []string
	Footer      BrandFooter
	UpdatedBy   uuid.UUID
	UpdatedAt   *time.Time
}

// EmptyBrandKit es el kit de una empresa que no ha guardado ninguno.
func EmptyBrandKit(tenantID uuid.UUID) *BrandKit {
	return &BrandKit{TenantID: tenantID, Colors: []string{}, Fonts: []string{}}
}

// NormalizeBrandKit valida el kit y lo devuelve con los colores en mayusculas y los textos sin
// espacios de borde. Los errores van envueltos en ErrInvalidBrandKit.
func NormalizeBrandKit(k BrandKit) (BrandKit, error) {
	if len(k.Colors) > MaxBrandColors {
		return k, fmt.Errorf("%w: colors admite hasta %d", ErrInvalidBrandKit, MaxBrandColors)
	}
	colors := make([]string, 0, len(k.Colors))
	seenColor := map[string]bool{}
	for i, c := range k.Colors {
		c = strings.ToUpper(strings.TrimSpace(c))
		if !brandColorRegex.MatchString(c) {
			return k, fmt.Errorf("%w: colors[%d] debe tener la forma #RRGGBB", ErrInvalidBrandKit, i)
		}
		if seenColor[c] {
			return k, fmt.Errorf("%w: el color %s esta repetido", ErrInvalidBrandKit, c)
		}
		seenColor[c] = true
		colors = append(colors, c)
	}
	if len(k.Fonts) > MaxBrandFonts {
		return k, fmt.Errorf("%w: fonts admite hasta %d", ErrInvalidBrandKit, MaxBrandFonts)
	}
	fonts := make([]string, 0, len(k.Fonts))
	seenFont := map[string]bool{}
	for i, f := range k.Fonts {
		f = strings.TrimSpace(f)
		if !isBrandFont(f) {
			return k, fmt.Errorf("%w: fonts[%d] %q no esta en la lista de tipografias seguras para correo", ErrInvalidBrandKit, i, f)
		}
		if seenFont[f] {
			return k, fmt.Errorf("%w: la tipografia %q esta repetida", ErrInvalidBrandKit, f)
		}
		seenFont[f] = true
		fonts = append(fonts, f)
	}
	k.Colors, k.Fonts = colors, fonts

	footer := BrandFooter{
		Company:      strings.TrimSpace(k.Footer.Company),
		Address:      strings.TrimSpace(k.Footer.Address),
		Website:      strings.TrimSpace(k.Footer.Website),
		SupportEmail: strings.TrimSpace(k.Footer.SupportEmail),
	}
	for _, f := range []struct {
		field, value string
		max          int
	}{
		{"footer.company", footer.Company, MaxFooterCompany},
		{"footer.address", footer.Address, MaxFooterAddress},
		{"footer.website", footer.Website, MaxFooterWebsite},
		{"footer.support_email", footer.SupportEmail, MaxFooterSupportEmail},
	} {
		if utf8.RuneCountInString(f.value) > f.max {
			return k, fmt.Errorf("%w: %s supera %d caracteres", ErrInvalidBrandKit, f.field, f.max)
		}
		if strings.ContainsAny(f.value, "<>") || strings.ContainsFunc(f.value, isControlExceptNewline) {
			return k, fmt.Errorf("%w: %s contiene caracteres no admitidos", ErrInvalidBrandKit, f.field)
		}
	}
	if footer.Website != "" {
		u, err := url.Parse(footer.Website)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return k, fmt.Errorf("%w: footer.website debe ser una URL absoluta http o https", ErrInvalidBrandKit)
		}
	}
	if footer.SupportEmail != "" {
		addr, err := mail.ParseAddress(footer.SupportEmail)
		if err != nil || addr.Address != footer.SupportEmail {
			return k, fmt.Errorf("%w: footer.support_email debe ser un correo valido", ErrInvalidBrandKit)
		}
	}
	k.Footer = footer
	return k, nil
}

// isControlExceptNewline admite saltos de linea (la direccion puede ir en varias lineas) y
// rechaza el resto de caracteres de control.
func isControlExceptNewline(r rune) bool {
	return r != '\n' && (r < 0x20 || r == 0x7f)
}
