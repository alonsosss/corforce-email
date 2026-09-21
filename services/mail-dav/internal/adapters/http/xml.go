package http

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	nsDAV     = "DAV:"
	nsCardDAV = "urn:ietf:params:xml:ns:carddav"
	nsCalDAV  = "urn:ietf:params:xml:ns:caldav"
	nsCS      = "http://calendarserver.org/ns/"
)

func davName(local string) xml.Name  { return xml.Name{Space: nsDAV, Local: local} }
func cardName(local string) xml.Name { return xml.Name{Space: nsCardDAV, Local: local} }
func calName(local string) xml.Name  { return xml.Name{Space: nsCalDAV, Local: local} }

// element es un nodo de respuesta con nombre y contenido dinamicos. encoding/xml solo escribe
// texto y atributos escapados: ninguna cadena del cliente llega al XML sin pasar por el.
type element struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Children []element  `xml:",any"`
}

func el(name xml.Name, text string, children ...element) element {
	return element{XMLName: name, Text: text, Children: children}
}

func hrefText(path string) string { return (&url.URL{Path: path}).EscapedPath() }

func hrefEl(path string) element { return el(davName("href"), hrefText(path)) }

type multistatus struct {
	XMLName   xml.Name   `xml:"DAV: multistatus"`
	Responses []response `xml:"DAV: response"`
	SyncToken string     `xml:"DAV: sync-token,omitempty"`
}

type response struct {
	Href      string     `xml:"DAV: href"`
	Propstats []propstat `xml:"DAV: propstat,omitempty"`
	Status    string     `xml:"DAV: status,omitempty"`
}

type propstat struct {
	Prop   propBody `xml:"DAV: prop"`
	Status string   `xml:"DAV: status"`
}

type propBody struct {
	Items []element `xml:",any"`
}

func statusLine(code int) string {
	return "HTTP/1.1 " + strconv.Itoa(code) + " " + http.StatusText(code)
}

// marshalXML serializa entero antes de escribir nada: un fallo a mitad de la respuesta dejaria un 207
// truncado que el cliente lee como una libreta vacia.
func marshalXML(v any) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	if err := xml.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeXML(w http.ResponseWriter, status int, v any) error {
	body, err := marshalXML(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
	return nil
}

type errorBody struct {
	XMLName xml.Name  `xml:"DAV: error"`
	Items   []element `xml:",any"`
}

// writeDAVError responde con el cuerpo de precondicion de WebDAV (RFC 4918, 16): un elemento dentro de
// DAV:error. Sin condicion (zero) responde solo el codigo.
func writeDAVError(w http.ResponseWriter, status int, condition xml.Name, children ...element) {
	if condition.Local == "" {
		w.WriteHeader(status)
		return
	}
	if err := writeXML(w, status, errorBody{Items: []element{{XMLName: condition, Children: children}}}); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// Los cuerpos de peticion son estructuras sin recursion: encoding/xml no expande entidades externas ni
// definidas en un DTD (una entidad desconocida es un error), y lo desconocido se salta sin acumular.

type xmlName struct {
	XMLName xml.Name
}

type propList struct {
	Items []xmlName `xml:",any"`
}

func (p *propList) names() []xml.Name {
	if p == nil {
		return nil
	}
	out := make([]xml.Name, 0, len(p.Items))
	for _, i := range p.Items {
		out = append(out, i.XMLName)
	}
	return out
}

type propfindReq struct {
	XMLName  xml.Name  `xml:"DAV: propfind"`
	AllProp  *struct{} `xml:"DAV: allprop"`
	PropName *struct{} `xml:"DAV: propname"`
	Prop     *propList `xml:"DAV: prop"`
}

type multigetReq struct {
	Prop  *propList `xml:"DAV: prop"`
	Hrefs []string  `xml:"DAV: href"`
}

type queryReq struct {
	Prop   *propList  `xml:"DAV: prop"`
	Filter *filterReq `xml:"urn:ietf:params:xml:ns:carddav filter"`
	Limit  *struct {
		NResults int `xml:"urn:ietf:params:xml:ns:carddav nresults"`
	} `xml:"urn:ietf:params:xml:ns:carddav limit"`
}

type filterReq struct {
	Test        string          `xml:"test,attr"`
	PropFilters []propFilterReq `xml:"urn:ietf:params:xml:ns:carddav prop-filter"`
}

type propFilterReq struct {
	Name         string         `xml:"name,attr"`
	Test         string         `xml:"test,attr"`
	IsNotDefined *struct{}      `xml:"urn:ietf:params:xml:ns:carddav is-not-defined"`
	TextMatches  []textMatchReq `xml:"urn:ietf:params:xml:ns:carddav text-match"`
	ParamFilters []xmlName      `xml:"urn:ietf:params:xml:ns:carddav param-filter"`
}

type textMatchReq struct {
	Text      string `xml:",chardata"`
	Collation string `xml:"collation,attr"`
	MatchType string `xml:"match-type,attr"`
	Negate    string `xml:"negate-condition,attr"`
}

type syncReq struct {
	Token string    `xml:"DAV: sync-token"`
	Level string    `xml:"DAV: sync-level"`
	Prop  *propList `xml:"DAV: prop"`
}

// settableProps son las propiedades que un cliente fija al crear una coleccion (MKCOL extendido, RFC 5689, o
// MKCALENDAR, RFC 4791). Las que el servidor no guarda (color, orden) se ignoran.
type settableProps struct {
	ResourceType *struct {
		Items []xmlName `xml:",any"`
	} `xml:"DAV: resourcetype"`
	DisplayName            *string `xml:"DAV: displayname"`
	AddressbookDescription *string `xml:"urn:ietf:params:xml:ns:carddav addressbook-description"`
	CalendarDescription    *string `xml:"urn:ietf:params:xml:ns:caldav calendar-description"`
	Components             *struct {
		Comps []struct {
			Name string `xml:"name,attr"`
		} `xml:"urn:ietf:params:xml:ns:caldav comp"`
	} `xml:"urn:ietf:params:xml:ns:caldav supported-calendar-component-set"`
}

type setBlock struct {
	Prop settableProps `xml:"DAV: prop"`
}

type mkcolReq struct {
	XMLName xml.Name   `xml:"DAV: mkcol"`
	Set     []setBlock `xml:"DAV: set"`
}

type mkcalendarReq struct {
	XMLName xml.Name   `xml:"urn:ietf:params:xml:ns:caldav mkcalendar"`
	Set     []setBlock `xml:"DAV: set"`
}

var errEmptyBody = errors.New("cuerpo vacio")

// decodeRoot lee el primer elemento del cuerpo y, segun su nombre, decodifica el resto en el destino
// que elija choose (nil si no lo admite).
func decodeRoot(body io.Reader, choose func(xml.Name) any) (xml.Name, error) {
	dec := xml.NewDecoder(body)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return xml.Name{}, errEmptyBody
		}
		if err != nil {
			return xml.Name{}, err
		}
		if start, ok := tok.(xml.StartElement); ok {
			dst := choose(start.Name)
			if dst == nil {
				return start.Name, nil
			}
			return start.Name, dec.DecodeElement(dst, &start)
		}
	}
}

func decodeDoc(body io.Reader, dst any) error {
	err := xml.NewDecoder(body).Decode(dst)
	if errors.Is(err, io.EOF) {
		return errEmptyBody
	}
	return err
}

// splitPath separa un path con escapes en sus segmentos ya decodificados. Rechaza segmentos vacios,
// "." y "..", separadores o caracteres de control en un segmento (incluidos los que llegan como %2F o
// %00): el path no se usa para tocar ningun fichero, pero cada segmento identifica un recurso y no puede
// decir otra cosa segun quien lo lea.
func splitPath(escaped string) ([]string, bool) {
	trimmed := strings.Trim(escaped, "/")
	if trimmed == "" {
		return nil, true
	}
	parts := strings.Split(trimmed, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		seg, err := url.PathUnescape(part)
		if err != nil || seg == "" || seg == "." || seg == ".." || len(seg) > 320 || strings.ContainsAny(seg, "/\\") {
			return nil, false
		}
		for i := 0; i < len(seg); i++ {
			if seg[i] < 0x20 || seg[i] == 0x7f {
				return nil, false
			}
		}
		out = append(out, seg)
	}
	return out, true
}
