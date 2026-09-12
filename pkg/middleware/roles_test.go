package middleware

import (
	"context"
	"testing"
)

func ctxConRoles(roles ...string) context.Context {
	return context.WithValue(context.Background(), CtxRoles, roles)
}

func TestHasAnyRole(t *testing.T) {
	casos := []struct {
		nombre    string
		roles     []string
		permitido []string
		quiere    bool
	}{
		{"rol exacto", []string{"operador_correo"}, []string{"auditor", "operador_correo"}, true},
		{"uno de varios", []string{"editor", "auditor"}, []string{"auditor", "operador_correo"}, true},
		{"sin coincidencia", []string{"editor"}, []string{"auditor", "operador_correo"}, false},
		{"sin roles", nil, []string{"auditor"}, false},
		// El superadmin de plataforma pasa siempre, como en RequireRoles: si no,
		// cada llamada nueva tendria que acordarse de anadirlo a su lista.
		{"superadmin siempre pasa", []string{RoleSuperadmin}, []string{"auditor"}, true},
		{"lista vacia solo deja al superadmin", []string{RoleTenantAdmin}, nil, false},
	}
	for _, c := range casos {
		t.Run(c.nombre, func(t *testing.T) {
			if got := HasAnyRole(ctxConRoles(c.roles...), c.permitido...); got != c.quiere {
				t.Fatalf("HasAnyRole(%v, %v) = %v; se esperaba %v", c.roles, c.permitido, got, c.quiere)
			}
		})
	}
}

// Los dos roles del sistema son los unicos privilegiados: cualquier otro, aunque se
// llame "admin", es un rol de empresa cuyos permisos viven en la base.
func TestSoloLosRolesDelSistemaSonPrivilegiados(t *testing.T) {
	for _, rol := range []string{RoleSuperadmin, RoleTenantAdmin} {
		if !IsPrivileged(ctxConRoles(rol)) {
			t.Fatalf("%s deberia ser privilegiado", rol)
		}
	}
	for _, rol := range []string{"admin", "owner", "administrador", ""} {
		if IsPrivileged(ctxConRoles(rol)) {
			t.Fatalf("%q no deberia ser privilegiado", rol)
		}
	}
	if IsPrivileged(context.Background()) {
		t.Fatal("sin roles no hay privilegio")
	}
}

func TestTenantAuthorityRolesExcluyeAlSuperadmin(t *testing.T) {
	for _, rol := range TenantAuthorityRoles() {
		if rol == RoleSuperadmin {
			t.Fatal("el superadmin no dirige ninguna empresa")
		}
	}
}
