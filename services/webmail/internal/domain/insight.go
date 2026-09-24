package domain

// InsightSource es lo que el buzon entrega de un mensaje para la ficha del remitente: remitente,
// Reply-To y las cabeceras de la bandeja inteligente, el escudo y la baja.
type InsightSource struct {
	From    []Address
	ReplyTo []Address
	Headers MessageHeaders
}

// SenderInsight es la ficha de un mensaje recibido: quien lo envia, en que pestana cae, lo que dice
// el escudo antifraude y como darse de baja si es un boletin.
type SenderInsight struct {
	Sender      *Address
	Category    Category
	Shield      Shield
	Unsubscribe Unsubscribe
}

// InsightHeaderKeys son las cabeceras que se piden al servidor para la ficha.
var InsightHeaderKeys = []string{
	HeaderListID, HeaderListUnsubscribe, HeaderListUnsubscribePost, HeaderPrecedence,
	HeaderAutoSubmitted, HeaderAuthResults, HeaderSpamdResult,
}

// CategoryHeaderKeys son las que decide la categoria en el listado.
var CategoryHeaderKeys = []string{
	HeaderListID, HeaderListUnsubscribe, HeaderPrecedence, HeaderAutoSubmitted,
}
