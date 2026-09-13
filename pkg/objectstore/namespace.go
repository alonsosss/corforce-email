package objectstore

import (
	"fmt"
	"os"
	"strings"
)

// FromEnvNamespace construye un Store para un vertical que necesita su PROPIO
// bucket, aislado del compartido.
//
// La diferencia con FromEnv no es de comodidad: **el bucket nunca cae al
// compartido**. Si <PREFIJO>_S3_BUCKET no esta definido devuelve (nil, nil) y el
// servicio degrada -sin poder guardar ni servir esos objetos- en lugar de
// escribir en el bucket de otra cosa. Un fallback silencioso ahi mezclaria los
// objetos aislados con los adjuntos de la plataforma y nadie lo notaria hasta que
// hubiera que borrar uno de los dos.
//
// Lo que SI se hereda del entorno compartido cuando no se declara aparte es el
// transporte: endpoint, region y credenciales. Son la direccion del servicio S3
// y la identidad de la maquina -en AWS, el rol de la instancia, con las claves
// vacias-, no una frontera de datos. Cada uno se puede fijar por separado con
// <PREFIJO>_S3_* cuando el aislamiento deba llegar tambien a la credencial.
//
// Variables, con PREFIJO en mayusculas:
//
//	<PREFIJO>_S3_BUCKET      obligatoria; sin ella no hay almacen
//	<PREFIJO>_S3_ENDPOINT    opcional, cae a MINIO_ENDPOINT
//	<PREFIJO>_S3_ACCESS_KEY  opcional, cae a MINIO_ACCESS_KEY
//	<PREFIJO>_S3_SECRET_KEY  opcional, cae a MINIO_SECRET_KEY
//	<PREFIJO>_S3_REGION      opcional, cae a MINIO_REGION
//	<PREFIJO>_S3_USE_SSL     opcional, cae a MINIO_USE_SSL
func FromEnvNamespace(prefijo string) (*Store, error) {
	prefijo = strings.ToUpper(strings.TrimSpace(prefijo))
	if prefijo == "" {
		return nil, fmt.Errorf("objectstore: prefijo de espacio requerido")
	}

	// El bucket es la frontera. No hereda.
	bucket := strings.TrimSpace(os.Getenv(prefijo + "_S3_BUCKET"))
	if bucket == "" {
		return nil, nil
	}

	endpoint := propio(prefijo, "ENDPOINT", "MINIO_ENDPOINT")
	if endpoint == "" {
		return nil, fmt.Errorf("objectstore: %s_S3_BUCKET definido sin endpoint; "+
			"declara %s_S3_ENDPOINT o MINIO_ENDPOINT", prefijo, prefijo)
	}

	return New(Config{
		Endpoint:   endpoint,
		AccessKey:  propio(prefijo, "ACCESS_KEY", "MINIO_ACCESS_KEY"),
		SecretKey:  propio(prefijo, "SECRET_KEY", "MINIO_SECRET_KEY"),
		Bucket:     bucket,
		Region:     propio(prefijo, "REGION", "MINIO_REGION"),
		PublicBase: resolvePublicBase(),
		UseSSL:     propio(prefijo, "USE_SSL", "MINIO_USE_SSL") == "true",
	})
}

// propio devuelve la variable del vertical si existe, y si no la compartida.
func propio(prefijo, sufijo, compartida string) string {
	if v := strings.TrimSpace(os.Getenv(prefijo + "_S3_" + sufijo)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(compartida))
}
