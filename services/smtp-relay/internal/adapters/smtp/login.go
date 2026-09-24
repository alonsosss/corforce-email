package smtp

import (
	"errors"

	"github.com/emersion/go-sasl"
)

// go-sasl solo trae el cliente de LOGIN (el mecanismo esta obsoleto, draft-murchison-sasl-login),
// pero muchas bibliotecas de envio solo saben ese. El servidor pide el usuario y la contrasena por
// separado; admite el usuario como respuesta inicial (AUTH LOGIN <base64>).
type loginServer struct {
	authenticate func(username, password string) error
	username     string
	step         int
}

var errLoginSequence = errors.New("secuencia de AUTH LOGIN invalida")

const (
	loginAskUser = iota
	loginAskPassword
	loginDone
)

func newLoginServer(authenticate func(username, password string) error) sasl.Server {
	return &loginServer{authenticate: authenticate}
}

func (s *loginServer) Next(response []byte) ([]byte, bool, error) {
	switch s.step {
	case loginAskUser:
		if response == nil {
			return []byte("Username:"), false, nil
		}
		s.username = string(response)
		s.step = loginAskPassword
		return []byte("Password:"), false, nil
	case loginAskPassword:
		s.step = loginDone
		if err := s.authenticate(s.username, string(response)); err != nil {
			return nil, true, err
		}
		return nil, true, nil
	}
	return nil, true, errLoginSequence
}
