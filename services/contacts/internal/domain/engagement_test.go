package domain

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func engagementEvent(kind EngagementKind, at time.Time) EngagementEvent {
	return EngagementEvent{TenantID: uuid.New(), ContactID: uuid.New(), CampaignID: uuid.New(), Kind: kind, OccurredAt: at}
}

func TestToqueDeInteraccion(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	retention := 400 * 24 * time.Hour

	got, ok, err := engagementEvent(EngagementDelivered, at).Touch(now, retention)
	if err != nil || !ok || !got.ReceivedAt.Equal(at) || got.OpenedAt != nil || got.ClickedAt != nil {
		t.Fatalf("entrega: %+v %v %v", got, ok, err)
	}
	got, ok, err = engagementEvent(EngagementOpened, at).Touch(now, retention)
	if err != nil || !ok || got.OpenedAt == nil || !got.OpenedAt.Equal(at) || got.ClickedAt != nil {
		t.Fatalf("apertura: %+v %v %v", got, ok, err)
	}
	got, ok, err = engagementEvent(EngagementClicked, at).Touch(now, retention)
	if err != nil || !ok || got.ClickedAt == nil || got.OpenedAt == nil || !got.OpenedAt.Equal(at) {
		t.Fatalf("un clic cuenta tambien como apertura: %+v %v %v", got, ok, err)
	}
}

func TestToqueDeInteraccionRecortaYDescarta(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	retention := 30 * 24 * time.Hour

	got, ok, err := engagementEvent(EngagementOpened, now.Add(48*time.Hour)).Touch(now, retention)
	if err != nil || !ok || !got.ReceivedAt.Equal(now) || !got.OpenedAt.Equal(now) {
		t.Fatalf("una hora futura se recorta a la actual: %+v", got)
	}
	got, ok, err = engagementEvent(EngagementOpened, now.Add(2*time.Minute)).Touch(now, retention)
	if err != nil || !ok || !got.ReceivedAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("un desfase pequeno de reloj se respeta: %+v", got)
	}
	got, ok, err = engagementEvent(EngagementDelivered, time.Time{}).Touch(now, retention)
	if err != nil || !ok || !got.ReceivedAt.Equal(now) {
		t.Fatalf("sin hora vale la actual: %+v", got)
	}
	if _, ok, err := engagementEvent(EngagementClicked, now.Add(-31*24*time.Hour)).Touch(now, retention); err != nil || ok {
		t.Fatalf("lo anterior a la retencion no se guarda: %v %v", ok, err)
	}
}

func TestToqueDeInteraccionInvalido(t *testing.T) {
	now := time.Now().UTC()
	for name, ev := range map[string]EngagementEvent{
		"sin empresa":  {ContactID: uuid.New(), CampaignID: uuid.New(), Kind: EngagementOpened},
		"sin contacto": {TenantID: uuid.New(), CampaignID: uuid.New(), Kind: EngagementOpened},
		"sin campana":  {TenantID: uuid.New(), ContactID: uuid.New(), Kind: EngagementOpened},
		"sin tipo":     {TenantID: uuid.New(), ContactID: uuid.New(), CampaignID: uuid.New()},
		"tipo ajeno":   {TenantID: uuid.New(), ContactID: uuid.New(), CampaignID: uuid.New(), Kind: "bounced"},
	} {
		if _, _, err := ev.Touch(now, 0); !errors.Is(err, ErrInvalidEngagement) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestAniversarioPorZonaYHora(t *testing.T) {
	lima := mustZone(t, "America/Lima")       // UTC-5
	tokio := mustZone(t, "Asia/Tokyo")        // UTC+9
	kiri := mustZone(t, "Pacific/Kiritimati") // UTC+14
	// 2026-09-23 03:00 UTC: en Lima aun es el 22 a las 22:00; en Tokio, el 23 a las 12:00;
	// en Kiritimati, el 23 a las 17:00.
	now := time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		loc   *time.Location
		hour  int
		occ   string
		ok    bool
	}{
		{"Lima aun en la vispera", "1990-09-23", lima, 9, "", false},
		{"Lima el dia anterior ya a su hora", "1990-09-22", lima, 9, "2026-09-22", true},
		{"Lima antes de la hora", "1990-09-22", lima, 23, "", false},
		{"Tokio a su hora", "1985-09-23", tokio, 9, "2026-09-23", true},
		{"Tokio antes de la hora", "1985-09-23", tokio, 13, "", false},
		{"Tokio a medianoche", "1985-09-23", tokio, 0, "2026-09-23", true},
		{"Kiritimati", "2000-09-23", kiri, 17, "2026-09-23", true},
		{"otro dia", "2000-09-24", tokio, 0, "", false},
		{"otro mes", "2000-10-23", tokio, 0, "", false},
		{"valor que no es fecha", "23/09/2000", tokio, 0, "", false},
		{"fecha imposible", "2000-02-30", tokio, 0, "", false},
	}
	for _, tc := range cases {
		occ, ok := AnniversaryOccurrence(tc.value, tc.loc, now, tc.hour)
		if ok != tc.ok || occ != tc.occ {
			t.Errorf("%s: %q %v, se esperaba %q %v", tc.name, occ, ok, tc.occ, tc.ok)
		}
	}
	if _, ok := AnniversaryOccurrence("1990-09-23", nil, now, 0); ok {
		t.Error("sin zona no hay aniversario")
	}
}

func TestAniversarioDelVeintinueveDeFebrero(t *testing.T) {
	utc := time.UTC
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 10, 0, 0, 0, utc) }
	cases := []struct {
		name string
		now  time.Time
		occ  string
		ok   bool
	}{
		{"ano bisiesto: el 29", at(2028, time.February, 29), "2028-02-29", true},
		{"ano bisiesto: no el 28", at(2028, time.February, 28), "", false},
		{"ano no bisiesto: el 28", at(2026, time.February, 28), "2026-02-28", true},
		{"ano no bisiesto: no el 1 de marzo", at(2026, time.March, 1), "", false},
		{"1900 no es bisiesto", at(1900, time.February, 28), "1900-02-28", true},
		{"2000 si es bisiesto", at(2000, time.February, 28), "", false},
		{"2100 no es bisiesto", at(2100, time.February, 28), "2100-02-28", true},
	}
	for _, tc := range cases {
		occ, ok := AnniversaryOccurrence("1992-02-29", utc, tc.now, 9)
		if ok != tc.ok || occ != tc.occ {
			t.Errorf("%s: %q %v, se esperaba %q %v", tc.name, occ, ok, tc.occ, tc.ok)
		}
	}
	if _, ok := AnniversaryOccurrence("1990-02-28", utc, at(2028, time.February, 29), 0); ok {
		t.Error("un 28 de febrero no se celebra el 29")
	}
	if occ, ok := AnniversaryOccurrence("1990-02-28", utc, at(2026, time.February, 28), 0); !ok || occ != "2026-02-28" {
		t.Error("un 28 de febrero sigue siendo el 28 en un ano no bisiesto")
	}
}

func TestCandidatosDeAniversario(t *testing.T) {
	got := AnniversaryCandidates(time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC))
	if !slices.Equal(got, []string{"09-22", "09-23", "09-24"}) {
		t.Fatalf("candidatos: %v", got)
	}
	got = AnniversaryCandidates(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if !slices.Equal(got, []string{"02-28", "02-29", "03-01", "03-02"}) {
		t.Fatalf("el 28 de un ano no bisiesto arrastra el 29: %v", got)
	}
	got = AnniversaryCandidates(time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC))
	if !slices.Equal(got, []string{"02-28", "02-29", "03-01"}) {
		t.Fatalf("ano bisiesto: %v", got)
	}
	// Todo aniversario que cae hoy en alguna zona esta entre los candidatos.
	for _, name := range []string{"Pacific/Kiritimati", "Etc/GMT+12", "America/Lima", "Asia/Kolkata", "UTC"} {
		loc := mustZone(t, name)
		for h := 0; h < 48; h++ {
			now := time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC).Add(time.Duration(h) * time.Hour)
			local := now.In(loc).Format("01-02")
			if !slices.Contains(AnniversaryCandidates(now), local) {
				t.Fatalf("%s %s: %s fuera de %v", name, now, local, AnniversaryCandidates(now))
			}
		}
	}
}
