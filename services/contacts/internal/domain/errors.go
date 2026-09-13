package domain

import "errors"

var (
	ErrContactNotFound   = errors.New("contacto no encontrado")
	ErrContactExists     = errors.New("ya existe un contacto con esa direccion en esta empresa")
	ErrListNotFound      = errors.New("lista no encontrada")
	ErrListExists        = errors.New("ya existe una lista con ese nombre")
	ErrListInUse         = errors.New("la lista la usa al menos un segmento")
	ErrSegmentNotFound   = errors.New("segmento no encontrado")
	ErrSegmentExists     = errors.New("ya existe un segmento con ese nombre")
	ErrAttributeNotFound = errors.New("atributo no declarado")
	ErrAttributeExists   = errors.New("el atributo ya esta declarado")
	ErrAttributeInUse    = errors.New("el atributo lo usa al menos un segmento")
	ErrTooManyAttributes = errors.New("se alcanzo el maximo de atributos declarados por empresa")
	ErrImportNotFound    = errors.New("importacion no encontrada")

	ErrInvalidEmail         = errors.New("direccion de correo no valida")
	ErrEmailImmutable       = errors.New("la direccion de un contacto no se modifica: crea un contacto nuevo")
	ErrInvalidLocale        = errors.New("locale debe ser una etiqueta BCP 47 corta (es, es-PE, pt-BR)")
	ErrInvalidTimezone      = errors.New("timezone debe ser una zona IANA valida (America/Lima)")
	ErrInvalidTag           = errors.New("etiqueta no valida: de 1 a 64 letras, digitos, espacio, _ . : -")
	ErrTooManyTags          = errors.New("un contacto admite como maximo 100 etiquetas")
	ErrInvalidName          = errors.New("el nombre admite como maximo 200 caracteres")
	ErrInvalidStatus        = errors.New("status debe ser active, unsubscribed, bounced o complained")
	ErrInvalidSource        = errors.New("source no valido")
	ErrInvalidAttributeKey  = errors.New("clave de atributo no valida: minusculas, digitos y _, empezando por letra, hasta 63")
	ErrReservedAttributeKey = errors.New("la clave coincide con un campo fijo del contacto")
	ErrInvalidAttributeType = errors.New("type debe ser string, number, boolean o date")
	ErrUndeclaredAttribute  = errors.New("atributo no declarado")
	ErrAttributeValue       = errors.New("valor de atributo no valido para su tipo")
	ErrRequiredAttribute    = errors.New("falta un atributo obligatorio")

	ErrInvalidConsentStatus = errors.New("status de consentimiento no valido")
	ErrInvalidConsentMethod = errors.New("method de consentimiento no valido")
	ErrInvalidIP            = errors.New("ip no valida")
	// ErrResubscribeRequiresOptIn: la persona se dio de baja o retiro su consentimiento.
	// Una empresa no puede volver a suscribirla por API: hace falta un formulario con la
	// ip de quien lo envio o el doble opt-in.
	ErrResubscribeRequiresOptIn = errors.New("volver a suscribir a quien se dio de baja exige un formulario con ip o el doble opt-in")
	ErrConsentAlreadyGranted    = errors.New("el contacto ya tiene el consentimiento concedido")
	ErrContactNotReachable      = errors.New("el contacto rebota o se quejo: no se le puede pedir confirmacion")
	// ErrInvalidConfirmation cubre token inexistente, usado, caducado o de otra empresa:
	// quien confirma no debe poder distinguir un caso de otro.
	ErrInvalidConfirmation = errors.New("enlace de confirmacion no valido")

	ErrInvalidListName    = errors.New("name es obligatorio y admite como maximo 200 caracteres")
	ErrInvalidSegmentName = errors.New("name es obligatorio y admite como maximo 200 caracteres")
	ErrTooManyMembers     = errors.New("contact_ids admite como maximo 1000 contactos por peticion")
	ErrInvalidLimit       = errors.New("limit debe estar entre 1 y 1000")

	ErrInvalidSegment  = errors.New("definicion de segmento no valida")
	ErrInvalidCursor   = errors.New("cursor no valido")
	ErrNoImportRows    = errors.New("rows no puede estar vacio")
	ErrTooManyRows     = errors.New("la importacion supera el maximo de filas")
	ErrConsentBasis    = errors.New("consent.basis es obligatorio cuando consent.status es granted")
	ErrInvalidAudience = errors.New("la audiencia necesita al menos una lista o un segmento, y admite como maximo 100 de cada")
)
