package objectstore

import "testing"

// El bucket de un espacio aislado NO puede caer al compartido. Es la unica garantia que
// sostiene el aislamiento: sin ella, olvidar una variable de entorno hace que sus
// objetos acaben conviviendo con los adjuntos de la plataforma, y eso no falla en el
// momento sino el dia que alguien vacia uno de los dos.
func TestBucketNoHeredaElCompartido(t *testing.T) {
	t.Setenv("MINIO_ENDPOINT", "s3.us-east-1.amazonaws.com")
	t.Setenv("MINIO_BUCKET", "plataforma-compartido")
	t.Setenv("MINIO_REGION", "us-east-1")

	st, err := FromEnvNamespace("AISLADO")
	if err != nil {
		t.Fatalf("sin bucket propio no deberia ser un error, sino degradar: %v", err)
	}
	if st != nil {
		t.Fatalf("sin AISLADO_S3_BUCKET el almacen debe quedar deshabilitado, no apuntar a %q", st.Bucket())
	}
}

func TestBucketPropioSeUsaYNoElCompartido(t *testing.T) {
	t.Setenv("MINIO_ENDPOINT", "s3.us-east-1.amazonaws.com")
	t.Setenv("MINIO_BUCKET", "plataforma-compartido")
	t.Setenv("MINIO_REGION", "us-east-1")
	t.Setenv("AISLADO_S3_BUCKET", "espacio-aislado")

	st, err := FromEnvNamespace("AISLADO")
	if err != nil {
		t.Fatalf("no se pudo construir el almacen: %v", err)
	}
	if st == nil {
		t.Fatal("con AISLADO_S3_BUCKET definido tiene que haber almacen")
	}
	if st.Bucket() != "espacio-aislado" {
		t.Fatalf("bucket = %q, se esperaba el propio del espacio", st.Bucket())
	}
}

// El transporte si se hereda: endpoint y region son la direccion del servicio,
// no una frontera de datos. Lo que no se hereda es el bucket.
func TestElTransporteSeHeredaPeroSePuedeFijarAparte(t *testing.T) {
	t.Setenv("MINIO_ENDPOINT", "s3.us-east-1.amazonaws.com")
	t.Setenv("MINIO_REGION", "us-east-1")
	t.Setenv("AISLADO_S3_BUCKET", "espacio-aislado")
	t.Setenv("AISLADO_S3_ENDPOINT", "s3.us-west-2.amazonaws.com")
	t.Setenv("AISLADO_S3_REGION", "us-west-2")

	st, err := FromEnvNamespace("AISLADO")
	if err != nil {
		t.Fatalf("no se pudo construir el almacen: %v", err)
	}
	if st == nil {
		t.Fatal("tiene que haber almacen")
	}
	if st.bucket != "espacio-aislado" || st.region != "us-west-2" {
		t.Fatalf("bucket=%q region=%q; el espacio debe poder fijar los suyos", st.bucket, st.region)
	}
}

// Un bucket declarado sin endpoint en ninguna parte es un error de
// configuracion, no un almacen deshabilitado: alguien quiso aislarlo y se quedo
// a medias, y arrancar en silencio esconderia justo eso.
func TestBucketSinEndpointEsError(t *testing.T) {
	t.Setenv("MINIO_ENDPOINT", "")
	t.Setenv("AISLADO_S3_BUCKET", "espacio-aislado")

	if _, err := FromEnvNamespace("AISLADO"); err == nil {
		t.Fatal("se esperaba error por bucket sin endpoint")
	}
}
