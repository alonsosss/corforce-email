package domain

import "errors"

var (
	ErrContactNotFound   = errors.New("contacto no encontrado")
	ErrContactExists     = errors.New("ya existe un contacto con esa dirección en esta empresa")
	ErrListNotFound      = errors.New("lista no encontrada")
	ErrListExists        = errors.New("ya existe una lista con ese nombre")
	ErrListInUse         = errors.New("la lista la usa al menos un segmento")
	ErrSegmentNotFound   = errors.New("segmento no encontrado")
	ErrSegmentExists     = errors.New("ya existe un segmento con ese nombre")
	ErrAttributeNotFound = errors.New("atributo no declarado")
	ErrAttributeExists   = errors.New("el atributo ya está declarado")
	ErrAttributeInUse    = errors.New("el atributo lo usa al menos un segmento")
	ErrTooManyAttributes = errors.New("se alcanzó el máximo de atributos declarados por empresa")
	ErrImportNotFound    = errors.New("importación no encontrada")

	ErrInvalidEmail         = errors.New("dirección de correo no válida")
	ErrEmailImmutable       = errors.New("la dirección de un contacto no se modifica: crea un contacto nuevo")
	ErrInvalidLocale        = errors.New("locale debe ser una etiqueta BCP 47 corta (es, es-PE, pt-BR)")
	ErrInvalidTimezone      = errors.New("timezone debe ser una zona IANA válida (America/Lima)")
	ErrInvalidTag           = errors.New("etiqueta no válida: de 1 a 64 letras, dígitos, espacio, _ . : -")
	ErrTooManyTags          = errors.New("un contacto admite como máximo 100 etiquetas")
	ErrInvalidName          = errors.New("el nombre admite como máximo 200 caracteres")
	ErrInvalidStatus        = errors.New("status no válido: los admitidos están en GET /contacts/meta")
	ErrInvalidSource        = errors.New("source no válido")
	ErrInvalidAttributeKey  = errors.New("clave de atributo no válida: minúsculas, dígitos y _, empezando por letra, hasta 63")
	ErrReservedAttributeKey = errors.New("la clave coincide con un campo fijo del contacto")
	ErrInvalidAttributeType = errors.New("type debe ser string, number, boolean o date")
	ErrUndeclaredAttribute  = errors.New("atributo no declarado")
	ErrAttributeValue       = errors.New("valor de atributo no válido para su tipo")
	ErrRequiredAttribute    = errors.New("falta un atributo obligatorio")

	ErrInvalidConsentStatus = errors.New("status de consentimiento no válido")
	ErrInvalidConsentMethod = errors.New("method de consentimiento no válido")
	ErrInvalidIP            = errors.New("ip no válida")
	// ErrResubscribeRequiresOptIn: la persona se dio de baja o retiro su consentimiento.
	// Una empresa no puede volver a suscribirla por API: hace falta un formulario con la
	// ip de quien lo envio o el doble opt-in.
	ErrResubscribeRequiresOptIn = errors.New("volver a suscribir a quien se dio de baja exige un formulario con ip o el doble opt-in")
	ErrConsentAlreadyGranted    = errors.New("el contacto ya tiene el consentimiento concedido")
	ErrContactNotReachable      = errors.New("la dirección está excluida de todo envío (rebote, queja, dirección no válida o exclusión manual): no se le puede pedir confirmación")
	// ErrInvalidConfirmation cubre token inexistente, usado, caducado o de otra empresa:
	// quien confirma no debe poder distinguir un caso de otro.
	ErrInvalidConfirmation = errors.New("enlace de confirmación no válido")

	ErrInvalidListName    = errors.New("name es obligatorio y admite como máximo 200 caracteres")
	ErrInvalidSegmentName = errors.New("name es obligatorio y admite como máximo 200 caracteres")
	ErrTooManyMembers     = errors.New("contact_ids admite como máximo 1000 contactos por petición")
	// ErrInvalidContactIDs: una consulta interna por ids sin ninguno o con mas de los que
	// admite la operacion.
	ErrInvalidContactIDs = errors.New("contact_ids no puede estar vacío ni superar el máximo de la operación")
	ErrInvalidLimit      = errors.New("limit debe estar entre 1 y 1000")

	ErrInvalidSegment  = errors.New("definición de segmento no válida")
	ErrInvalidCursor   = errors.New("cursor no válido")
	ErrNoImportRows    = errors.New("rows no puede estar vacío")
	ErrTooManyRows     = errors.New("la importación supera el máximo de filas")
	ErrConsentBasis    = errors.New("consent.basis es obligatorio cuando consent.status es granted")
	ErrInvalidAudience = errors.New("la audiencia necesita al menos una lista o un segmento, y admite como máximo 100 de cada")
)
