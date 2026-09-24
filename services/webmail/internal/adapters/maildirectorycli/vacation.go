package maildirectorycli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/alonsosss/corforce-email/services/webmail/internal/domain"
)

type vacationBody struct {
	Enabled      bool    `json:"enabled"`
	Subject      string  `json:"subject"`
	Message      string  `json:"message"`
	IntervalDays int     `json:"interval_days"`
	StartsOn     *string `json:"starts_on"`
	EndsOn       *string `json:"ends_on"`
}

type vacationResponse struct {
	Data struct {
		vacationBody
		UpdatedAt *time.Time `json:"updated_at"`
		Limits    struct {
			SubjectMaxLength int `json:"subject_max_length"`
			MessageMaxLength int `json:"message_max_length"`
			IntervalMinDays  int `json:"interval_min_days"`
			IntervalMaxDays  int `json:"interval_max_days"`
		} `json:"limits"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Vacation lee la respuesta automatica del buzon.
func (c *Client) Vacation(ctx context.Context, username string) (domain.Vacation, error) {
	return c.callVacation(ctx, http.MethodGet, username, nil)
}

// SetVacation la reemplaza. Un texto que el directorio rechaza (422) vuelve como
// *domain.ValidationError con el motivo del directorio, sin copiar su regla aqui; cualquier otro
// fallo es domain.ErrUnavailable. PUT reemplaza el estado entero, asi que repetirlo es seguro.
func (c *Client) SetVacation(ctx context.Context, username string, in domain.VacationInput) (domain.Vacation, error) {
	return c.callVacation(ctx, http.MethodPut, username, &vacationBody{
		Enabled: in.Enabled, Subject: in.Subject, Message: in.Message, IntervalDays: in.IntervalDays,
		StartsOn: in.StartsOn, EndsOn: in.EndsOn,
	})
}

func (c *Client) callVacation(ctx context.Context, method, username string, body *vacationBody) (domain.Vacation, error) {
	endpoint := c.vacation + "?" + url.Values{"username": {username}}.Encode()
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return domain.Vacation{}, fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return domain.Vacation{}, fmt.Errorf("%w: %v", domain.ErrUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Gateway-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return domain.Vacation{}, fmt.Errorf("%w: mail-directory: %v", domain.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	var decoded vacationResponse
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&decoded)
	if resp.StatusCode == http.StatusUnprocessableEntity && decodeErr == nil && decoded.Error != nil && decoded.Error.Message != "" {
		return domain.Vacation{}, domain.NewValidationError("vacation", decoded.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return domain.Vacation{}, fmt.Errorf("%w: mail-directory respondió %d", domain.ErrUnavailable, resp.StatusCode)
	}
	if decodeErr != nil {
		return domain.Vacation{}, fmt.Errorf("%w: respuesta de mail-directory ilegible", domain.ErrUnavailable)
	}
	d := decoded.Data
	return domain.Vacation{
		Enabled: d.Enabled, Subject: d.Subject, Message: d.Message, IntervalDays: d.IntervalDays,
		StartsOn: d.StartsOn, EndsOn: d.EndsOn, UpdatedAt: d.UpdatedAt,
		Limits: domain.VacationLimits{
			SubjectMaxLength: d.Limits.SubjectMaxLength, MessageMaxLength: d.Limits.MessageMaxLength,
			IntervalMinDays: d.Limits.IntervalMinDays, IntervalMaxDays: d.Limits.IntervalMaxDays,
		},
	}, nil
}
