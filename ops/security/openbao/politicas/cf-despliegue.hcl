# Materializar los secretos antes de levantar contenedores (fetch-secrets.sh). Solo lectura del
# documento vigente: ni versiones anteriores, ni metadatos, ni escritura.
path "cf/data/plataforma" {
  capabilities = ["read"]
}
