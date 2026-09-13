package app

import (
	"context"
	"sort"
	"strings"

	"github.com/alonsosss/corforce-email/services/mail-security/internal/domain"
	"github.com/alonsosss/corforce-email/services/mail-security/internal/ports"
)

// MaxExpansionHops acota la recursion de aliases. Un alias puede apuntar a otro alias,
// y nada impide que dos se apunten entre si: sin tope la expansion no termina.
const MaxExpansionHops = 20

// Expander resuelve una direccion de sobre hasta sus buzones finales siguiendo la misma
// logica que Postfix aplica con sus mapas (dominio alias -> destino, alias -> goto,
// catch-all @dominio), pero sin entregar: solo para saber a quien va a llegar.
type Expander struct {
	dir ports.DirectoryReader
}

func NewExpander(dir ports.DirectoryReader) *Expander { return &Expander{dir: dir} }

// Expand devuelve los buzones finales que reciben, sin duplicados y ordenados. Una
// direccion de un dominio ajeno o sin destino local devuelve una lista vacia sin error.
func (e *Expander) Expand(ctx context.Context, address string) ([]domain.Mailbox, error) {
	visited := map[string]struct{}{}
	found := map[string]domain.Mailbox{}
	if err := e.walk(ctx, domain.NormalizeAddress(address), 0, visited, found); err != nil {
		return nil, err
	}
	out := make([]domain.Mailbox, 0, len(found))
	for _, m := range found {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

// ExpandToSingle es lo que /aliasexp devuelve: el username solo si la expansion acaba
// en exactamente un buzon; vacio en cualquier otro caso.
func (e *Expander) ExpandToSingle(ctx context.Context, address string) (string, error) {
	boxes, err := e.Expand(ctx, address)
	if err != nil {
		return "", err
	}
	if len(boxes) != 1 {
		return "", nil
	}
	return boxes[0].Username, nil
}

func (e *Expander) walk(ctx context.Context, address string, depth int, visited map[string]struct{}, found map[string]domain.Mailbox) error {
	if depth > MaxExpansionHops {
		return domain.ErrExpansionTooDeep
	}
	if _, seen := visited[address]; seen {
		return nil
	}
	visited[address] = struct{}{}

	local, dom, ok := domain.SplitAddress(address)
	if !ok {
		return nil
	}
	if target, isAlias, err := e.dir.AliasDomainTarget(ctx, dom); err != nil {
		return err
	} else if isAlias {
		dom = target
		address = local + "@" + dom
		if _, seen := visited[address]; seen {
			return nil
		}
		visited[address] = struct{}{}
	}

	mb, err := e.dir.MailboxByUsername(ctx, address)
	if err != nil && err != domain.ErrNotFound {
		return err
	}
	if mb != nil {
		if mb.Receives() {
			found[mb.Username] = *mb
		}
		return nil
	}

	goto_, isAlias, err := e.dir.AliasGoto(ctx, address)
	if err != nil {
		return err
	}
	if !isAlias {
		goto_, isAlias, err = e.dir.AliasGoto(ctx, "@"+dom)
		if err != nil {
			return err
		}
		if !isAlias {
			return nil
		}
	}
	for _, dest := range strings.Split(goto_, ",") {
		dest = domain.NormalizeAddress(dest)
		if dest == "" || strings.HasSuffix(dest, "@localhost") {
			continue
		}
		if err := e.walk(ctx, dest, depth+1, visited, found); err != nil {
			if err == domain.ErrExpansionTooDeep {
				continue
			}
			return err
		}
	}
	return nil
}
