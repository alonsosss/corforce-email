package render

import (
	"github.com/alonsosss/corforce-email/services/templates/internal/domain"
	"github.com/alonsosss/corforce-email/services/templates/internal/ports"
)

// Port adapta Engine al puerto ports.Renderer del caso de uso. Existe porque Go no admite
// covarianza en el tipo de retorno: Compile devuelve *Compiled y el puerto pide la
// interfaz.
type Port struct {
	engine *Engine
}

func NewPort() *Port { return &Port{engine: New()} }

func (p *Port) Compile(c domain.Content) (ports.CompiledTemplate, error) {
	compiled, err := p.engine.Compile(c)
	if err != nil {
		return nil, err
	}
	return compiled, nil
}
