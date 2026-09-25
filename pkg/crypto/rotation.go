package crypto

import (
	"context"
	"errors"
	"fmt"
)

// SealedRecord es un dato cifrado guardado, con la clave por la que se recorre su tabla.
type SealedRecord[K any] struct {
	Key    K
	Sealed []byte
}

// SealedStore da acceso a lo cifrado de una tabla para re-cifrarlo bajo la llave activa.
type SealedStore[K any] interface {
	// SealedAfter devuelve hasta limit datos con clave mayor que after, en orden de clave.
	SealedAfter(ctx context.Context, after K, limit int) ([]SealedRecord[K], error)
	// ReplaceSealed guarda next en la fila de key solo si sigue guardando prev, en una sola
	// sentencia. false: la fila cambio o desaparecio entre tanto, y quien la cambio ya cifro con
	// la llave activa.
	ReplaceSealed(ctx context.Context, key K, prev, next []byte) (bool, error)
}

// RotationReport resume una pasada de RotateStore.
type RotationReport struct {
	// Examined son los datos leidos; Rotated, los re-cifrados bajo la llave activa; Changed,
	// los que otro escribio mientras se re-cifraban.
	Examined, Rotated, Changed int
	// Pending son los que no abre ninguna llave del anillo: mientras haya alguno, retirar una
	// llave vieja deja datos ilegibles para siempre.
	Pending int
}

// RotateStore recorre store entero y re-cifra bajo la llave activa lo que solo abre una llave
// retirada. aad da los datos autenticados con que se cifro cada fila. Es idempotente y no toma
// cerrojos: la sustitucion es condicional, asi que varias replicas pueden recorrer la misma tabla
// a la vez sin pisarse, y una escritura concurrente siempre gana. Un error corta la pasada y
// devuelve lo hecho hasta ahi.
func RotateStore[K any](ctx context.Context, kr *KeyRing, store SealedStore[K], start K, batch int, aad func(K) []byte) (RotationReport, error) {
	var rep RotationReport
	if batch <= 0 {
		return rep, errors.New("crypto: el lote de la rotacion debe ser positivo")
	}
	after := start
	for {
		recs, err := store.SealedAfter(ctx, after, batch)
		if err != nil {
			return rep, fmt.Errorf("crypto: leer lo cifrado: %w", err)
		}
		for _, r := range recs {
			rep.Examined++
			after = r.Key
			next, rotated, err := kr.RotateWithAAD(r.Sealed, aad(r.Key))
			if errors.Is(err, ErrUndecryptable) {
				rep.Pending++
				continue
			}
			if err != nil {
				return rep, fmt.Errorf("crypto: re-cifrar: %w", err)
			}
			if !rotated {
				continue
			}
			ok, err := store.ReplaceSealed(ctx, r.Key, r.Sealed, next)
			if err != nil {
				return rep, fmt.Errorf("crypto: guardar lo re-cifrado: %w", err)
			}
			if ok {
				rep.Rotated++
			} else {
				rep.Changed++
			}
		}
		if len(recs) < batch {
			return rep, nil
		}
	}
}
