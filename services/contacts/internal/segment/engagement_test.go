package segment

import (
	"fmt"
	"reflect"
	"testing"
)

const sampleCampaignID = "5f0c1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b"

func TestReglasDeInteraccion(t *testing.T) {
	cases := []struct {
		name string
		rule string
		sql  string
		args []any
	}{
		{"abrio una campana", `{"field":"campaign","op":"opened","value":"5F0C1A2B-3C4D-4E5F-8A9B-0C1D2E3F4A5B"}`,
			`EXISTS (SELECT 1 FROM contacts.engagement e WHERE e.contact_id = c.id AND e.campaign_id = $1::uuid AND e.last_opened_at IS NOT NULL)`,
			[]any{sampleCampaignID}},
		{"hizo clic en una campana", `{"field":"campaign","op":"clicked","value":"` + sampleCampaignID + `"}`,
			`EXISTS (SELECT 1 FROM contacts.engagement e WHERE e.contact_id = c.id AND e.campaign_id = $1::uuid AND e.last_clicked_at IS NOT NULL)`,
			[]any{sampleCampaignID}},
		{"abrio alguna de las ultimas N", `{"field":"last_campaigns","op":"opened","value":3}`,
			`EXISTS (SELECT 1 FROM (SELECT e.last_opened_at, e.last_clicked_at FROM contacts.engagement e WHERE e.contact_id = c.id ORDER BY e.received_at DESC LIMIT $1::int) t WHERE t.last_opened_at IS NOT NULL)`,
			[]any{3}},
		{"hizo clic en alguna de las ultimas N", `{"field":"last_campaigns","op":"clicked","value":5}`,
			`EXISTS (SELECT 1 FROM (SELECT e.last_opened_at, e.last_clicked_at FROM contacts.engagement e WHERE e.contact_id = c.id ORDER BY e.received_at DESC LIMIT $1::int) t WHERE t.last_clicked_at IS NOT NULL)`,
			[]any{5}},
		{"no abrio ninguna de las ultimas N", `{"field":"last_campaigns","op":"not_opened","value":4}`,
			`(SELECT count(*) = $1::int AND count(t.last_opened_at) = 0 FROM (SELECT e.last_opened_at, e.last_clicked_at FROM contacts.engagement e WHERE e.contact_id = c.id ORDER BY e.received_at DESC LIMIT $1::int) t)`,
			[]any{4}},
		{"abrio en los ultimos N dias", `{"field":"last_days","op":"opened","value":30}`,
			`EXISTS (SELECT 1 FROM contacts.engagement e WHERE e.contact_id = c.id AND e.last_opened_at >= now() - make_interval(days => $1::int))`,
			[]any{30}},
		{"hizo clic en los ultimos N dias", `{"field":"last_days","op":"clicked","value":7}`,
			`EXISTS (SELECT 1 FROM contacts.engagement e WHERE e.contact_id = c.id AND e.last_clicked_at >= now() - make_interval(days => $1::int))`,
			[]any{7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args := mustCompile(t, `{"match":"all","rules":[`+tc.rule+`]}`)
			if want := "(" + tc.sql + ")"; sql != want {
				t.Fatalf("sql\n got: %s\nwant: %s", sql, want)
			}
			if !reflect.DeepEqual(args, tc.args) {
				t.Fatalf("args\n got: %#v\nwant: %#v", args, tc.args)
			}
		})
	}
}

func TestReglasDeInteraccionInvalidas(t *testing.T) {
	for _, rule := range []string{
		`{"field":"campaign","op":"opened","value":"no-es-un-id"}`,
		`{"field":"campaign","op":"opened"}`,
		`{"field":"campaign","op":"not_opened","value":"` + sampleCampaignID + `"}`,
		`{"field":"campaign","op":"eq","value":"` + sampleCampaignID + `"}`,
		`{"field":"last_campaigns","op":"opened","value":0}`,
		`{"field":"last_campaigns","op":"opened","value":-2}`,
		`{"field":"last_campaigns","op":"opened","value":2.5}`,
		`{"field":"last_campaigns","op":"opened","value":"3"}`,
		fmt.Sprintf(`{"field":"last_campaigns","op":"opened","value":%d}`, MaxLastCampaigns+1),
		`{"field":"last_days","op":"not_opened","value":3}`,
		`{"field":"last_days","op":"opened","value":1e3}`,
		fmt.Sprintf(`{"field":"last_days","op":"clicked","value":%d}`, MaxLastDays+1),
		`{"field":"last_days","op":"opened","value":99999999999999999999}`,
	} {
		expectInvalid(t, `{"match":"all","rules":[`+rule+`]}`)
	}
	for _, rule := range []string{
		fmt.Sprintf(`{"field":"last_campaigns","op":"not_opened","value":%d}`, MaxLastCampaigns),
		fmt.Sprintf(`{"field":"last_days","op":"opened","value":%d}`, MaxLastDays),
		`{"field":"last_days","op":"opened","value":1}`,
	} {
		mustCompile(t, `{"match":"all","rules":[`+rule+`]}`)
	}
}

// Las reglas de interaccion se combinan con las demas y numeran sus placeholders a
// continuacion: la audiencia une varias definiciones en una sola consulta.
func TestInteraccionCombinada(t *testing.T) {
	def := mustParse(t, `{"match":"any","rules":[{"field":"status","op":"eq","value":"active"},{"field":"last_campaigns","op":"not_opened","value":2}]}`)
	args := NewArgs("tenant")
	sql, err := Compile(def, testSchema(), args)
	if err != nil {
		t.Fatal(err)
	}
	want := `(c.status = $2::text OR (SELECT count(*) = $3::int AND count(t.last_opened_at) = 0 FROM (SELECT e.last_opened_at, e.last_clicked_at FROM contacts.engagement e WHERE e.contact_id = c.id ORDER BY e.received_at DESC LIMIT $3::int) t))`
	if sql != want {
		t.Fatalf("sql\n got: %s\nwant: %s", sql, want)
	}
	if got := args.Values(); !reflect.DeepEqual(got, []any{"tenant", "active", 2}) {
		t.Fatalf("args: %#v", got)
	}
}

func TestCatalogoAnunciaLosTopesDeInteraccion(t *testing.T) {
	cat := Describe(testSchema())
	if cat.Limits.MaxLastCampaigns != MaxLastCampaigns || cat.Limits.MaxLastDays != MaxLastDays {
		t.Fatalf("limites: %+v", cat.Limits)
	}
	types := map[string]ValueType{}
	max := map[string]int{}
	for _, f := range cat.Fields {
		types[f.Field] = f.ValueType
		max[f.Field] = f.Max
	}
	if max["last_campaigns"] != MaxLastCampaigns || max["last_days"] != MaxLastDays || max["campaign"] != 0 || max["email"] != 0 {
		t.Fatalf("topes por campo: %v", max)
	}
	if types["campaign"] != ValueCampaign || types["last_campaigns"] != ValueCount || types["last_days"] != ValueCount {
		t.Fatalf("tipos de valor: %v", types)
	}
}
