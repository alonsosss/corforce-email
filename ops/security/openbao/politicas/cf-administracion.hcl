# Operar el almacen: publicar y retirar secretos (add-secret.sh, remove-secret.sh, rotate-key.sh,
# push-secrets.sh), ver y recuperar versiones, y reaplicar la configuracion de instalar.sh. Se
# limita a lo que la plataforma crea (montaje cf, politicas y roles cf-*): no puede montar otros
# motores ni otros metodos de acceso, y no toca la auditoria, que es declarativa (config.hcl.tmpl).
path "cf/data/plataforma" {
  capabilities = ["create", "read", "update"]
}
path "cf/metadata/plataforma" {
  capabilities = ["read"]
}
path "cf/config" {
  capabilities = ["create", "read", "update"]
}
path "sys/mounts" {
  capabilities = ["read"]
}
path "sys/mounts/cf" {
  capabilities = ["create", "read", "update"]
}
path "sys/policies/acl/cf-*" {
  capabilities = ["create", "read", "update"]
}
path "sys/auth" {
  capabilities = ["read"]
}
path "sys/auth/approle" {
  capabilities = ["create", "read", "update", "sudo"]
}
path "auth/approle/role/cf-*" {
  capabilities = ["create", "read", "update"]
}
