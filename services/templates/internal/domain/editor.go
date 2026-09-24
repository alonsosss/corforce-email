package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// EditorKindGrapesJSMJML es el documento de GrapesJS con el complemento MJML (docs/adr/0012).
const EditorKindGrapesJSMJML = "grapesjs-mjml"

// MaxEditorBytes es el tope del documento del editor serializado en forma compacta.
const MaxEditorBytes = 2 << 20

func EditorKinds() []string { return []string{EditorKindGrapesJSMJML} }

// EditorDocument es el diseno del editor guardado junto a la version para volver a editarla.
// El servidor no lo interpreta: lo que se renderiza y se envia es el HTML de la version.
type EditorDocument struct {
	Kind    string          `json:"kind"`
	Project json.RawMessage `json:"project"`
	MJML    string          `json:"mjml"`
}

// NormalizeEditor valida el documento y lo devuelve con el proyecto compactado. nil significa
// que la version se escribio a mano.
func NormalizeEditor(e *EditorDocument) (*EditorDocument, error) {
	if e == nil {
		return nil, nil
	}
	if !contains(EditorKinds(), e.Kind) {
		return nil, fmt.Errorf("%w: editor.kind debe ser uno de: %s", ErrInvalidEditor, strings.Join(EditorKinds(), ", "))
	}
	project := bytes.TrimSpace(e.Project)
	if len(project) == 0 || project[0] != '{' {
		return nil, fmt.Errorf("%w: editor.project debe ser un objeto", ErrInvalidEditor)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, project); err != nil {
		return nil, fmt.Errorf("%w: editor.project no es JSON válido", ErrInvalidEditor)
	}
	out := &EditorDocument{Kind: e.Kind, Project: compact.Bytes(), MJML: e.MJML}
	serialized, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidEditor, err)
	}
	if len(serialized) > MaxEditorBytes {
		return nil, fmt.Errorf("%w: el documento del editor supera %d bytes", ErrInvalidEditor, MaxEditorBytes)
	}
	return out, nil
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
