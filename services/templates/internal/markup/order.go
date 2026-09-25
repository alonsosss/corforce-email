// Package markup arma el marcado estructurado que la plataforma anade a un correo al
// renderizarlo. Hoy solo la tarjeta de pedido de Gmail: schema.org Order en JSON-LD
// (https://developers.google.com/workspace/gmail/markup/reference/order).
//
// El marcado nunca lo escribe quien disena la plantilla: la plataforma lo genera con
// encoding/json a partir de los valores ya validados, asi que un dato no puede cerrar el
// <script> ni inyectar nada. Si faltan datos para un marcado valido, el correo sale sin el: es
// una mejora de presentacion, no una condicion del envio.
package markup

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/shopspring/decimal"
)

// Variables que lee el marcado de pedido. Son las de los bloques de pedido del editor.
const (
	VarOrderNumber  = "order_number"
	VarCurrencyCode = "currency_code"
	VarTotal        = "total"
	VarItems        = "items"
	VarOrderURL     = "order_url"
	VarStatus       = "status"

	FieldName      = "name"
	FieldQuantity  = "quantity"
	FieldUnitPrice = "unit_price"
	FieldImageURL  = "image_url"
)

type requirement struct {
	name string
	typ  string
}

var (
	orderRequired = []requirement{
		{VarOrderNumber, domain.VarString},
		{VarCurrencyCode, domain.VarString},
		{VarTotal, domain.VarNumber},
		{VarItems, domain.VarList},
	}
	orderOptional = []requirement{
		{VarOrderURL, domain.VarURL},
		{VarStatus, domain.VarString},
	}
	itemRequired = []requirement{
		{FieldName, domain.VarString},
		{FieldQuantity, domain.VarNumber},
		{FieldUnitPrice, domain.VarNumber},
	}
	itemOptional = []requirement{{FieldImageURL, domain.VarImage}}
)

// currencyCode: ISO 4217, lo que exige priceCurrency. El simbolo que se muestra (S/) va aparte.
var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// orderStatuses traduce los estados de los bloques de pedido a schema.org OrderStatus.
var orderStatuses = map[string]string{
	"received":  "http://schema.org/OrderProcessing",
	"preparing": "http://schema.org/OrderProcessing",
	"shipped":   "http://schema.org/OrderInTransit",
	"delivered": "http://schema.org/OrderDelivered",
}

// Validate comprueba que una version con marcado declara lo que el marcado necesita. El tipo de
// plantilla lo comprueba el caso de uso: el marcado de pedido es solo transaccional.
func Validate(markup string, vars []domain.Variable) error {
	switch markup {
	case "":
		return nil
	case domain.MarkupOrder:
		return validateOrder(vars)
	default:
		return fmt.Errorf("%w: %q no existe; se admite %s", domain.ErrInvalidMarkup, markup, strings.Join(domain.Markups(), ", "))
	}
}

func validateOrder(vars []domain.Variable) error {
	byName := make(map[string]domain.Variable, len(vars))
	for _, v := range vars {
		byName[v.Name] = v
	}
	// Requeridas y no solo declaradas: una opcional ausente vale el cero de su tipo y la tarjeta
	// mostraria un precio 0,00 que nadie envio.
	for _, r := range orderRequired {
		v, ok := byName[r.name]
		if !ok || v.Type != r.typ || !v.Required {
			return fmt.Errorf("%w: la tarjeta de pedido necesita la variable requerida %q de tipo %s", domain.ErrInvalidMarkup, r.name, r.typ)
		}
	}
	for _, r := range orderOptional {
		if v, ok := byName[r.name]; ok && v.Type != r.typ {
			return fmt.Errorf("%w: la variable %q debe ser de tipo %s para la tarjeta de pedido", domain.ErrInvalidMarkup, r.name, r.typ)
		}
	}
	items := byName[VarItems]
	for _, r := range itemRequired {
		f, ok := items.Field(r.name)
		if !ok || f.Type != r.typ || !f.Required {
			return fmt.Errorf("%w: la lista %q necesita el campo requerido %q de tipo %s", domain.ErrInvalidMarkup, VarItems, r.name, r.typ)
		}
	}
	for _, r := range itemOptional {
		if f, ok := items.Field(r.name); ok && f.Type != r.typ {
			return fmt.Errorf("%w: el campo %q de %q debe ser de tipo %s", domain.ErrInvalidMarkup, r.name, VarItems, r.typ)
		}
	}
	return nil
}

// Build devuelve el JSON-LD del marcado con los valores ya resueltos, o false si faltan datos
// para uno valido. merchant es el nombre del comercio (el del kit de marca o el de la empresa).
func Build(markup string, values map[string]any, merchant string) (string, bool) {
	if markup != domain.MarkupOrder {
		return "", false
	}
	return buildOrder(values, strings.TrimSpace(merchant))
}

type organization struct {
	Type string `json:"@type"`
	Name string `json:"name"`
}

type product struct {
	Type  string `json:"@type"`
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

type quantity struct {
	Type  string `json:"@type"`
	Value string `json:"value"`
}

type offer struct {
	Type             string   `json:"@type"`
	ItemOffered      product  `json:"itemOffered"`
	Price            string   `json:"price"`
	PriceCurrency    string   `json:"priceCurrency"`
	EligibleQuantity quantity `json:"eligibleQuantity"`
}

type order struct {
	Context       string       `json:"@context"`
	Type          string       `json:"@type"`
	Merchant      organization `json:"merchant"`
	OrderNumber   string       `json:"orderNumber"`
	PriceCurrency string       `json:"priceCurrency"`
	Price         string       `json:"price"`
	URL           string       `json:"url,omitempty"`
	OrderStatus   string       `json:"orderStatus,omitempty"`
	AcceptedOffer []offer      `json:"acceptedOffer"`
}

func buildOrder(values map[string]any, merchant string) (string, bool) {
	number := text(values[VarOrderNumber])
	code := text(values[VarCurrencyCode])
	total, okTotal := amount(values[VarTotal])
	items, _ := values[VarItems].([]map[string]any)
	if merchant == "" || number == "" || !currencyCode.MatchString(code) || !okTotal || len(items) == 0 {
		return "", false
	}
	doc := order{
		Context: "http://schema.org", Type: "Order",
		Merchant:    organization{Type: "Organization", Name: merchant},
		OrderNumber: number, PriceCurrency: code, Price: total,
		URL:         text(values[VarOrderURL]),
		OrderStatus: orderStatuses[text(values[VarStatus])],
	}
	for _, item := range items {
		name := text(item[FieldName])
		price, okPrice := amount(item[FieldUnitPrice])
		qty, okQty := amount(item[FieldQuantity])
		if name == "" || !okPrice || !okQty {
			return "", false
		}
		doc.AcceptedOffer = append(doc.AcceptedOffer, offer{
			Type:             "Offer",
			ItemOffered:      product{Type: "Product", Name: name, Image: text(item[FieldImageURL])},
			Price:            price,
			PriceCurrency:    code,
			EligibleQuantity: quantity{Type: "QuantitativeValue", Value: strings.TrimRight(strings.TrimRight(qty, "0"), ".")},
		})
	}
	// json.Marshal escapa <, > y & como secuencias \u: ningun valor puede cerrar el <script>.
	b, err := json.Marshal(doc)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func text(v any) string {
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s)
	case fmt.Stringer:
		return strings.TrimSpace(s.String())
	default:
		return ""
	}
}

// amount escribe un numero ya validado con dos decimales, sin pasar por float64.
func amount(v any) (string, bool) {
	s := text(v)
	if s == "" {
		return "", false
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return "", false
	}
	return d.StringFixed(2), true
}

var headClose = regexp.MustCompile(`(?i)</head\s*>`)

// Inject anade el JSON-LD al documento: al final del <head> o, si no lo hay, al principio.
func Inject(html, jsonLD string) string {
	block := `<script type="application/ld+json">` + jsonLD + `</script>`
	if loc := headClose.FindStringIndex(html); loc != nil {
		return html[:loc[0]] + block + html[loc[0]:]
	}
	return block + html
}
