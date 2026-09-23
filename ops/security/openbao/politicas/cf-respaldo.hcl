# Instantanea de raft para el respaldo (ops/backup/backup-tenants.sh). La instantanea va cifrada
# con la llave de OpenBao; sin la llave de desbloqueo no se lee.
path "sys/storage/raft/snapshot" {
  capabilities = ["read"]
}
