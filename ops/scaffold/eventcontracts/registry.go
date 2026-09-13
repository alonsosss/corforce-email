package main

import (
	"fmt"
	"sort"
	"strings"
)

// registry es docs/arquitectura/EVENTS.md: quien publica y quien consume cada subject,
// sacado del mismo analisis que los contratos para que los dos documentos no discrepen.
type registry struct {
	rows     []registryRow
	services []serviceEntry
	npub     int
	ncon     int
	orphans  []string // subjects o patrones consumidos que nadie publica
}

type registryRow struct {
	subject    string
	publishers []string
	consumers  []string
}

type serviceEntry struct {
	name      string
	publishes []string
	consumes  []string
}

func buildRegistry(res *result) registry {
	pubs := map[string]map[string]bool{} // subject -> servicios
	subs := map[string]map[string]bool{} // subject o patron -> servicios
	svcPub := map[string]map[string]bool{}
	svcSub := map[string]map[string]bool{}
	add := func(m map[string]map[string]bool, k, v string) {
		if m[k] == nil {
			m[k] = map[string]bool{}
		}
		m[k][v] = true
	}
	for _, p := range res.pubs {
		add(pubs, p.Subject, p.Service)
		add(svcPub, p.Service, p.Subject)
	}
	for _, c := range res.cons {
		add(subs, c.Subject, c.Consumer)
		add(svcSub, c.Consumer, c.Subject)
	}

	var reg registry
	for _, m := range svcPub {
		reg.npub += len(m)
	}
	for _, m := range svcSub {
		reg.ncon += len(m)
	}

	// Un patron con comodin no es un subject: sus suscriptores se listan en cada subject
	// publicado que reciben, y el patron solo tiene fila propia si no casa con ninguno.
	rows := map[string]bool{}
	for s := range pubs {
		rows[s] = true
	}
	for pattern := range subs {
		covered := false
		for s := range pubs {
			if matchSubject(pattern, s) {
				covered = true
				break
			}
		}
		if !covered {
			rows[pattern] = true
			reg.orphans = append(reg.orphans, pattern)
		}
	}
	sort.Strings(reg.orphans)

	for _, s := range sortedKeys(rows) {
		consumers := map[string]bool{}
		for pattern, svcs := range subs {
			if pattern == s || (!isPattern(s) && matchSubject(pattern, s)) {
				for svc := range svcs {
					consumers[svc] = true
				}
			}
		}
		reg.rows = append(reg.rows, registryRow{subject: s, publishers: sortedKeys(pubs[s]), consumers: sortedKeys(consumers)})
	}
	for _, name := range res.services {
		if len(svcPub[name]) == 0 && len(svcSub[name]) == 0 {
			continue
		}
		reg.services = append(reg.services, serviceEntry{name: name,
			publishes: sortedKeys(svcPub[name]), consumes: sortedKeys(svcSub[name])})
	}
	return reg
}

func (reg registry) render() string {
	var b strings.Builder
	b.WriteString("# Registro de contratos de eventos (NATS)\n\n")
	b.WriteString("Generado por `ops/scaffold/gen-events.sh` desde el codigo. NO editar a mano.\n")
	b.WriteString("Convencion de subject: `<dominio>.<entidad>.<accion>`. Un subject tiene UN dueno\n")
	b.WriteString("(el servicio que lo publica); los demas solo lo consumen (regla no-fork).\n")
	b.WriteString("Publicar incluye encolar en la outbox (`outbox.Enqueue`); un consumidor con comodin\n")
	b.WriteString("(`*`, `>`) figura en cada subject publicado que recibe.\n\n")
	fmt.Fprintf(&b, "Resumen: %d publicaciones, %d suscripciones, %d subjects distintos.\n\n", reg.npub, reg.ncon, len(reg.rows))
	b.WriteString("## Cruce por subject (dueno -> consumidores)\n\n")
	b.WriteString("| Subject | Publica | Consumen |\n|---|---|---|\n")
	for _, r := range reg.rows {
		pubs := strings.Join(r.publishers, ", ")
		if pubs == "" {
			pubs = "(ninguno: subject huerfano)"
		}
		cons := strings.Join(r.consumers, ", ")
		if cons == "" {
			cons = "-"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", r.subject, pubs, cons)
	}
	b.WriteString("\n## Por servicio\n\n")
	for _, s := range reg.services {
		fmt.Fprintf(&b, "### %s\n", s.name)
		if len(s.publishes) > 0 {
			fmt.Fprintf(&b, "- Publica: %s\n", codeList(s.publishes))
		}
		if len(s.consumes) > 0 {
			fmt.Fprintf(&b, "- Consume: %s\n", codeList(s.consumes))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func codeList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, it := range items {
		quoted = append(quoted, "`"+it+"`")
	}
	return strings.Join(quoted, ", ")
}

func isPattern(subject string) bool {
	for _, tok := range strings.Split(subject, ".") {
		if tok == "*" || tok == ">" {
			return true
		}
	}
	return false
}

// matchSubject aplica la semantica de NATS: "*" casa un token y ">" uno o mas al final.
func matchSubject(pattern, subject string) bool {
	pt, st := strings.Split(pattern, "."), strings.Split(subject, ".")
	for i, p := range pt {
		if p == ">" {
			return len(st) > i
		}
		if i >= len(st) || (p != "*" && p != st[i]) {
			return false
		}
	}
	return len(pt) == len(st)
}
