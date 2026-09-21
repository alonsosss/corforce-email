package mailreconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/alonsosss/corforce-email/pkg/config"
	"github.com/alonsosss/corforce-email/pkg/middleware"
	"github.com/alonsosss/corforce-email/pkg/tenantcell"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	existencePath   = "/internal/mail-directory/mailboxes/existence"
	maxResponseBody = 64 << 10

	mailDirectoryURLEnv       = "MAIL_DIRECTORY_URL"
	mailDirectoryCellHostsEnv = "MAIL_DIRECTORY_CELL_HOSTS"
)

// DirectoryClient consulta a mail-directory, en la celda de cada empresa (tenantcell.Caller: token de
// gateway, X-Tenant-ID y la instancia que sirve a la celda), que buzones de la empresa existen.
type DirectoryClient struct {
	cell *tenantcell.Caller
}

func NewDirectory(cell *tenantcell.Caller) *DirectoryClient { return &DirectoryClient{cell: cell} }

// NewDirectoryFromEnv arma el cliente con la misma configuracion de celdas que el resto de servicios que
// llaman a mail-directory: MAIL_DIRECTORY_URL, las instancias por celda de MAIL_DIRECTORY_CELL_HOSTS y, con
// varias celdas, ORGANIZATION_URL para resolver la celda de cada empresa.
func NewDirectoryFromEnv(logger *zap.Logger) (*DirectoryClient, error) {
	directoryURL, err := config.RequiredServiceURL(mailDirectoryURLEnv)
	if err != nil {
		return nil, err
	}
	token, err := middleware.InternalGatewayToken()
	if err != nil {
		return nil, err
	}
	cells, err := tenantcell.LoadInstances(os.Getenv, tenantcell.BaseCellEnv, mailDirectoryCellHostsEnv)
	if err != nil {
		return nil, err
	}
	var resolver *tenantcell.Resolver
	if cells.BaseCell != "" {
		orgURL, err := tenantcell.OrganizationURLFromEnv()
		if err != nil {
			return nil, err
		}
		resolver = tenantcell.NewResolver(orgURL, token, logger)
	}
	targets, err := cells.Targets(mailDirectoryCellHostsEnv, directoryURL, resolver)
	if err != nil {
		return nil, err
	}
	return NewDirectory(tenantcell.NewCaller("mail-directory", targets, token, logger, tenantcell.CallerOptions{})), nil
}

type existenceRequest struct {
	IDs []uuid.UUID `json:"ids"`
}

type existenceResponse struct {
	Data struct {
		// Existing es un puntero para distinguir la lista vacia (ningun buzon existe) de una respuesta sin
		// lista: la segunda no dice nada y no debe leerse como "todos borrados".
		Existing *[]uuid.UUID `json:"existing"`
	} `json:"data"`
}

// Existing hace POST /internal/mail-directory/mailboxes/existence. Solo un 200 con un cuerpo legible es una
// respuesta: cualquier otra cosa es un error, que el barrido trata como "no se sabe" y nunca como "no existe".
// De la respuesta solo cuentan los ids que se preguntaron.
func (c *DirectoryClient) Existing(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]struct{}, error) {
	body, err := json.Marshal(existenceRequest{IDs: ids})
	if err != nil {
		return nil, err
	}
	resp, err := c.cell.Do(ctx, tenantID, http.MethodPost, existencePath, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mail-directory respondio %d a la consulta de existencia", resp.StatusCode)
	}
	var out existenceResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("mail-directory: respuesta de existencia ilegible: %w", err)
	}
	if out.Data.Existing == nil {
		return nil, fmt.Errorf("mail-directory: la respuesta de existencia no trae la lista de buzones")
	}
	asked := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		asked[id] = struct{}{}
	}
	existing := make(map[uuid.UUID]struct{}, len(*out.Data.Existing))
	for _, id := range *out.Data.Existing {
		if _, ok := asked[id]; ok {
			existing[id] = struct{}{}
		}
	}
	return existing, nil
}
