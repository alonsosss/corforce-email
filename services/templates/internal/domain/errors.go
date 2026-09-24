package domain

import "errors"

var (
	ErrTemplateNotFound  = errors.New("plantilla no encontrada")
	ErrVersionNotFound   = errors.New("versión no encontrada")
	ErrTemplateNameTaken = errors.New("ya existe una plantilla con ese nombre")
	// ErrTemplateNotArchived: una plantilla se borra solo despues de archivarla, para que
	// el borrado nunca sea un accidente de un clic.
	ErrTemplateNotArchived = errors.New("la plantilla debe estar archivada para borrarse")
	// ErrTemplateArchived: una plantilla archivada no admite versiones nuevas, publicar ni
	// renderizar envios; sigue pudiendo previsualizarse.
	ErrTemplateArchived        = errors.New("la plantilla está archivada")
	ErrVersionAlreadyPublished = errors.New("la versión ya está publicada")
	ErrNoPublishedVersion      = errors.New("la plantilla no tiene ninguna versión publicada")
	ErrVersionNotPublished     = errors.New("la versión no ha sido publicada; use la previsualización")
	ErrNothingToUpdate         = errors.New("la petición no trae ningún campo que actualizar")
	ErrInvalidName             = errors.New("nombre de plantilla no válido")
	ErrInvalidKind             = errors.New("tipo de plantilla no válido")
	ErrInvalidTemplateStatus   = errors.New("estado de plantilla no válido")
	ErrMissingCreator          = errors.New("la operación exige un usuario autenticado")
	// ErrInvalidVariableDeclaration envuelve los defectos de la lista de variables
	// declaradas: nombre, tipo, duplicados, nombre reservado o default incoherente.
	ErrInvalidVariableDeclaration = errors.New("declaración de variables no válida")

	// ErrInvalidTemplate envuelve cualquier defecto del contenido detectado al compilar:
	// sintaxis, funcion no permitida, variable no declarada, HTML peligroso o tamano.
	ErrInvalidTemplate = errors.New("plantilla no válida")
	// ErrInvalidVariables envuelve los fallos de los valores recibidos al renderizar:
	// requerida ausente, tipo incorrecto, URL no absoluta.
	ErrInvalidVariables = errors.New("variables no válidas")
	// ErrOutputTooLarge: el renderizado supero el tamano maximo de salida.
	ErrOutputTooLarge = errors.New("la salida renderizada supera el tamaño máximo")

	ErrInvalidEditor   = errors.New("documento del editor no válido")
	ErrInvalidBrandKit = errors.New("kit de marca no válido")
	ErrInvalidAsset    = errors.New("imagen no válida")
	ErrAssetNotFound   = errors.New("imagen no encontrada")
	ErrAssetExists     = errors.New("la imagen ya existe en la empresa")
	ErrInvalidCursor   = errors.New("cursor de paginación no válido")
	// ErrAssetRejected: ClamAV encontro malware en la imagen; no se guarda.
	ErrAssetRejected = errors.New("la imagen fue rechazada por el análisis antivirus")
	// ErrScannerUnavailable: sin ClamAV configurado o sin respuesta limpia de el no se admite
	// ninguna subida (falla cerrado).
	ErrScannerUnavailable = errors.New("el análisis antivirus no está disponible")
	// ErrStorageUnavailable: el servicio arranco sin almacen de objetos.
	ErrStorageUnavailable = errors.New("el almacén de imágenes no está configurado")
	// ErrDeliverabilityFailed: la verificacion de entregabilidad encontro errores que impiden
	// publicar una version de marketing. Lo envuelve DeliverabilityError, que lleva el informe.
	ErrDeliverabilityFailed = errors.New("la versión no supera la verificación de entregabilidad")
)
