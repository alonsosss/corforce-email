package domain

import "errors"

var (
	ErrTemplateNotFound  = errors.New("plantilla no encontrada")
	ErrVersionNotFound   = errors.New("version no encontrada")
	ErrTemplateNameTaken = errors.New("ya existe una plantilla con ese nombre")
	// ErrTemplateNotArchived: una plantilla se borra solo despues de archivarla, para que
	// el borrado nunca sea un accidente de un clic.
	ErrTemplateNotArchived = errors.New("la plantilla debe estar archivada para borrarse")
	// ErrTemplateArchived: una plantilla archivada no admite versiones nuevas, publicar ni
	// renderizar envios; sigue pudiendo previsualizarse.
	ErrTemplateArchived        = errors.New("la plantilla esta archivada")
	ErrVersionAlreadyPublished = errors.New("la version ya esta publicada")
	ErrNoPublishedVersion      = errors.New("la plantilla no tiene ninguna version publicada")
	ErrVersionNotPublished     = errors.New("la version no ha sido publicada; use la previsualizacion")
	ErrNothingToUpdate         = errors.New("la peticion no trae ningun campo que actualizar")
	ErrInvalidName             = errors.New("nombre de plantilla no valido")
	ErrInvalidKind             = errors.New("tipo de plantilla no valido")
	ErrInvalidTemplateStatus   = errors.New("estado de plantilla no valido")
	ErrMissingCreator          = errors.New("la operacion exige un usuario autenticado")
	// ErrInvalidVariableDeclaration envuelve los defectos de la lista de variables
	// declaradas: nombre, tipo, duplicados, nombre reservado o default incoherente.
	ErrInvalidVariableDeclaration = errors.New("declaracion de variables no valida")

	// ErrInvalidTemplate envuelve cualquier defecto del contenido detectado al compilar:
	// sintaxis, funcion no permitida, variable no declarada, HTML peligroso o tamano.
	ErrInvalidTemplate = errors.New("plantilla no valida")
	// ErrInvalidVariables envuelve los fallos de los valores recibidos al renderizar:
	// requerida ausente, tipo incorrecto, URL no absoluta.
	ErrInvalidVariables = errors.New("variables no validas")
	// ErrOutputTooLarge: el renderizado supero el tamano maximo de salida.
	ErrOutputTooLarge = errors.New("la salida renderizada supera el tamano maximo")

	ErrInvalidEditor   = errors.New("documento del editor no valido")
	ErrInvalidBrandKit = errors.New("kit de marca no valido")
	ErrInvalidAsset    = errors.New("imagen no valida")
	ErrAssetNotFound   = errors.New("imagen no encontrada")
	ErrAssetExists     = errors.New("la imagen ya existe en la empresa")
	ErrInvalidCursor   = errors.New("cursor de paginacion no valido")
	// ErrAssetRejected: ClamAV encontro malware en la imagen; no se guarda.
	ErrAssetRejected = errors.New("la imagen fue rechazada por el analisis antivirus")
	// ErrScannerUnavailable: sin ClamAV configurado o sin respuesta limpia de el no se admite
	// ninguna subida (falla cerrado).
	ErrScannerUnavailable = errors.New("el analisis antivirus no esta disponible")
	// ErrStorageUnavailable: el servicio arranco sin almacen de objetos.
	ErrStorageUnavailable = errors.New("el almacen de imagenes no esta configurado")
	// ErrDeliverabilityFailed: la verificacion de entregabilidad encontro errores que impiden
	// publicar una version de marketing. Lo envuelve DeliverabilityError, que lleva el informe.
	ErrDeliverabilityFailed = errors.New("la version no supera la verificacion de entregabilidad")
)
