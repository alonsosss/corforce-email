package http

import "encoding/xml"

// Cuerpos de peticion de CalDAV (RFC 4791). Como los de CardDAV, son estructuras sin recursion: cada
// nivel de comp-filter que se admite es un tipo aparte, y todo elemento que no se reconoce se recoge en
// Others para que el filtro lo rechace en vez de ignorarlo (ignorar una condicion devolveria eventos que el
// cliente excluyo).

// calPropList es el prop de un informe de calendario. A diferencia de propList guarda los hijos de cada
// propiedad, porque calendar-data admite modificadores (expand, comp) que hay que ver.
type calPropList struct {
	Items []calPropItem `xml:",any"`
}

type calPropItem struct {
	XMLName  xml.Name
	Children []xmlName `xml:",any"`
}

func (l *calPropList) tooMany() bool { return l != nil && len(l.Items) > maxRequestedProps }

// names devuelve el prop como lista de nombres y los modificadores de calendar-data que el servidor no
// aplica. comp (recuperacion parcial) no se aplica y se admite: siempre se devuelve el objeto entero.
func (l *calPropList) names() (list *propList, unsupported bool) {
	if l == nil {
		return nil, false
	}
	list = &propList{Items: make([]xmlName, 0, len(l.Items))}
	for _, it := range l.Items {
		list.Items = append(list.Items, xmlName{XMLName: it.XMLName})
		if it.XMLName != nameCalendarData {
			continue
		}
		for _, child := range it.Children {
			switch child.XMLName {
			case calName("expand"), calName("limit-recurrence-set"), calName("limit-freebusy-set"):
				unsupported = true
			}
		}
	}
	return list, unsupported
}

type calMultigetReq struct {
	Prop  *calPropList `xml:"DAV: prop"`
	Hrefs []string     `xml:"DAV: href"`
}

type calQueryReq struct {
	Prop   *calPropList  `xml:"DAV: prop"`
	Filter *calFilterReq `xml:"urn:ietf:params:xml:ns:caldav filter"`
	Others []xmlName     `xml:",any"`
}

type calFilterReq struct {
	Comps  []calCompFilterReq `xml:"urn:ietf:params:xml:ns:caldav comp-filter"`
	Others []xmlName          `xml:",any"`
}

// calCompFilterReq es el comp-filter de primer nivel (VCALENDAR) y calSubFilterReq el de segundo (VEVENT
// y demas componentes). Un tercer nivel (VALARM) se detecta en Nested y se rechaza.
type calCompFilterReq struct {
	Name         string             `xml:"name,attr"`
	IsNotDefined *struct{}          `xml:"urn:ietf:params:xml:ns:caldav is-not-defined"`
	TimeRange    *timeRangeReq      `xml:"urn:ietf:params:xml:ns:caldav time-range"`
	PropFilters  []calPropFilterReq `xml:"urn:ietf:params:xml:ns:caldav prop-filter"`
	Subs         []calSubFilterReq  `xml:"urn:ietf:params:xml:ns:caldav comp-filter"`
	Others       []xmlName          `xml:",any"`
}

type calSubFilterReq struct {
	Name         string             `xml:"name,attr"`
	IsNotDefined *struct{}          `xml:"urn:ietf:params:xml:ns:caldav is-not-defined"`
	TimeRange    *timeRangeReq      `xml:"urn:ietf:params:xml:ns:caldav time-range"`
	PropFilters  []calPropFilterReq `xml:"urn:ietf:params:xml:ns:caldav prop-filter"`
	Nested       []xmlName          `xml:"urn:ietf:params:xml:ns:caldav comp-filter"`
	Others       []xmlName          `xml:",any"`
}

type timeRangeReq struct {
	Start string `xml:"start,attr"`
	End   string `xml:"end,attr"`
}

type calPropFilterReq struct {
	Name         string         `xml:"name,attr"`
	Test         string         `xml:"test,attr"`
	IsNotDefined *struct{}      `xml:"urn:ietf:params:xml:ns:caldav is-not-defined"`
	TextMatches  []textMatchReq `xml:"urn:ietf:params:xml:ns:caldav text-match"`
	TimeRange    *timeRangeReq  `xml:"urn:ietf:params:xml:ns:caldav time-range"`
	ParamFilters []xmlName      `xml:"urn:ietf:params:xml:ns:caldav param-filter"`
	Others       []xmlName      `xml:",any"`
}
