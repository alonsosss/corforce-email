package crypto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
)

func aadDe(k int) []byte { return []byte(fmt.Sprintf("fila-%d", k)) }

func TestRotateWithAADRecifraConLosMismosDatosAutenticados(t *testing.T) {
	anterior := anillo(t, llaveA, "")
	dato, err := anterior.EncryptWithAAD([]byte("secreto"), aadDe(1))
	if err != nil {
		t.Fatal(err)
	}
	rotando := anillo(t, llaveB, llaveA)
	nuevo, rotado, err := rotando.RotateWithAAD(dato, aadDe(1))
	if err != nil || !rotado {
		t.Fatalf("rotado=%v err=%v", rotado, err)
	}
	retirada := anillo(t, llaveB, "")
	if plano, err := retirada.DecryptWithAAD(nuevo, aadDe(1)); err != nil || string(plano) != "secreto" {
		t.Fatalf("tras retirar la vieja: %q %v", plano, err)
	}
	if _, err := retirada.DecryptWithAAD(nuevo, aadDe(2)); err == nil {
		t.Fatal("lo re-cifrado se abre con los datos de otra fila")
	}
	if otra, rotado, err := rotando.RotateWithAAD(nuevo, aadDe(1)); err != nil || rotado || !bytes.Equal(otra, nuevo) {
		t.Fatalf("segunda pasada: rotado=%v err=%v", rotado, err)
	}
	if _, _, err := rotando.RotateWithAAD(dato, aadDe(2)); !errors.Is(err, ErrUndecryptable) {
		t.Fatalf("con los datos de otra fila: %v, se esperaba ErrUndecryptable", err)
	}
}

// memStore es una tabla en memoria con la sustitucion condicional de SealedStore. before se
// ejecuta antes de cada sustitucion: simula a otro que escribe la fila entre tanto.
type memStore struct {
	rows   map[int][]byte
	before func(k int)
}

func (m *memStore) SealedAfter(_ context.Context, after, limit int) ([]SealedRecord[int], error) {
	keys := make([]int, 0, len(m.rows))
	for k := range m.rows {
		if k > after {
			keys = append(keys, k)
		}
	}
	sort.Ints(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]SealedRecord[int], 0, len(keys))
	for _, k := range keys {
		out = append(out, SealedRecord[int]{Key: k, Sealed: m.rows[k]})
	}
	return out, nil
}

func (m *memStore) ReplaceSealed(_ context.Context, k int, prev, next []byte) (bool, error) {
	if m.before != nil {
		m.before(k)
	}
	if !bytes.Equal(m.rows[k], prev) {
		return false, nil
	}
	m.rows[k] = next
	return true, nil
}

func TestRotateStoreRecorreTodoEsIdempotenteYCuentaLoIlegible(t *testing.T) {
	anterior := anillo(t, llaveA, "")
	ajeno := anillo(t, llaveC, "")
	store := &memStore{rows: map[int][]byte{}}
	for k := 1; k <= 7; k++ {
		kr := anterior
		if k == 5 {
			kr = ajeno
		}
		dato, err := kr.EncryptWithAAD([]byte(fmt.Sprintf("secreto-%d", k)), aadDe(k))
		if err != nil {
			t.Fatal(err)
		}
		store.rows[k] = dato
	}
	rotando := anillo(t, llaveB, llaveA)
	rep, err := RotateStore(context.Background(), rotando, store, 0, 3, aadDe)
	if err != nil {
		t.Fatal(err)
	}
	if rep != (RotationReport{Examined: 7, Rotated: 6, Pending: 1}) {
		t.Fatalf("primera pasada: %+v", rep)
	}
	retirada := anillo(t, llaveB, "")
	for k, dato := range store.rows {
		plano, err := retirada.DecryptWithAAD(dato, aadDe(k))
		if k == 5 {
			if err == nil {
				t.Fatal("el dato ajeno se abrio")
			}
			continue
		}
		if err != nil || string(plano) != fmt.Sprintf("secreto-%d", k) {
			t.Fatalf("fila %d: %q %v", k, plano, err)
		}
	}
	rep, err = RotateStore(context.Background(), rotando, store, 0, 3, aadDe)
	if err != nil || rep != (RotationReport{Examined: 7, Pending: 1}) {
		t.Fatalf("segunda pasada: %+v %v", rep, err)
	}
}

func TestRotateStoreNoPisaUnaEscrituraConcurrente(t *testing.T) {
	anterior := anillo(t, llaveA, "")
	dato, err := anterior.EncryptWithAAD([]byte("viejo"), aadDe(1))
	if err != nil {
		t.Fatal(err)
	}
	rotando := anillo(t, llaveB, llaveA)
	escrito, err := rotando.EncryptWithAAD([]byte("nuevo"), aadDe(1))
	if err != nil {
		t.Fatal(err)
	}
	store := &memStore{rows: map[int][]byte{1: dato}}
	store.before = func(k int) { store.rows[k] = escrito }
	rep, err := RotateStore(context.Background(), rotando, store, 0, 10, aadDe)
	if err != nil || rep != (RotationReport{Examined: 1, Changed: 1}) {
		t.Fatalf("%+v %v", rep, err)
	}
	if !bytes.Equal(store.rows[1], escrito) {
		t.Fatal("la rotacion piso lo escrito por otro")
	}
}

func TestRotateStoreRechazaUnLoteNoPositivo(t *testing.T) {
	if _, err := RotateStore(context.Background(), anillo(t, llaveB, ""), &memStore{}, 0, 0, aadDe); err == nil {
		t.Fatal("lote cero aceptado")
	}
}
