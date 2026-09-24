package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCarpetaDePospuestos(t *testing.T) {
	if RoleByName("snoozed") != RoleSnoozed || !IsSnoozedFolderName("SNOOZED") {
		t.Fatal("la carpeta de pospuestos se reconoce por el nombre")
	}
	if err := ValidateNewFolderName("Snoozed", "/"); err == nil {
		t.Fatal("una carpeta propia no puede llamarse como la de pospuestos")
	}
	if !ProtectedFolder(Folder{Name: SnoozedFolderName, Role: RoleSnoozed}) {
		t.Fatal("la carpeta de pospuestos no se renombra ni se borra")
	}
	found := false
	for _, r := range SpecialRoles {
		found = found || r == RoleSnoozed
	}
	if !found {
		t.Fatal("la interfaz recibe el papel snoozed en la meta")
	}
	for _, f := range []Folder{{Role: RoleSnoozed}, {Role: RoleScheduled}} {
		if err := CheckSnoozable(f); err == nil {
			t.Fatalf("%s no se pospone", f.Role)
		}
	}
	if err := CheckSnoozable(Folder{Name: "INBOX", Role: RoleInbox}); err != nil {
		t.Fatal(err)
	}
}

func TestHoraDeUnRecordatorio(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	casos := map[string]time.Time{
		"cero":      {},
		"inmediata": now.Add(30 * time.Second),
		"lejana":    now.AddDate(0, 0, 31),
	}
	for nombre, at := range casos {
		var verr *ValidationError
		if err := ValidateReminderAt("until", at, now, 30); !errors.As(err, &verr) || verr.Field != "until" {
			t.Fatalf("%s: %v", nombre, err)
		}
	}
	if err := ValidateReminderAt("until", now.Add(2*time.Minute), now, 30); err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{0, 31} {
		if err := ValidateFollowUpDays(days, 30); err == nil {
			t.Fatalf("%d dias", days)
		}
	}
	if err := ValidateFollowUpDays(7, 30); err != nil {
		t.Fatal(err)
	}
}

func TestUIDsDeUnLote(t *testing.T) {
	uids, err := NormalizeUIDs([]uint32{3, 3, 1})
	if err != nil || len(uids) != 2 || uids[0] != 3 || uids[1] != 1 {
		t.Fatalf("%v %v", uids, err)
	}
	for _, bad := range [][]uint32{nil, {0}, make([]uint32, MaxBatchUIDs+1)} {
		if _, err := NormalizeUIDs(bad); err == nil {
			t.Fatalf("%d uids", len(bad))
		}
	}
}

func TestDireccionesDeUnRecordatorio(t *testing.T) {
	in := []string{" a@b.pe ", ""}
	for i := 0; i < MaxReminderAddresses+5; i++ {
		in = append(in, "x@y.pe")
	}
	out := ReminderAddresses(in)
	if len(out) != MaxReminderAddresses || out[0] != "a@b.pe" {
		t.Fatalf("%d %q", len(out), out[0])
	}
}

func TestRespuestaRapidaDeLaInterfaz(t *testing.T) {
	casos := map[string]QuickReplyInput{
		"name": {Name: "  ", HTML: "x"},
		"html": {Name: "Hola", HTML: " "},
	}
	for campo, in := range casos {
		var verr *ValidationError
		if err := in.Validate(); !errors.As(err, &verr) || verr.Field != campo {
			t.Fatalf("%s: %v", campo, err)
		}
	}
	if err := (QuickReplyInput{Name: "Hola", HTML: "<p>" + strings.Repeat("{nombre}", 3) + "</p>"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if ValidateQuickReplyID("x") == nil || ValidateReminderID("../x") == nil {
		t.Fatal("ids invalidos")
	}
}
