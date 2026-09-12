// Package sqlsource inspecciona el SQL escrito a mano dentro del codigo Go.
//
// No sustituye a probar contra una base -- eso se hace aparte, aplicando las
// migraciones a una desechable -- pero cubre un hueco real: una condicion que
// desaparece de un WHERE compila igual, pasa el tipado igual y cambia lo que la
// consulta hace. Las comprobaciones construidas sobre esto fallan cuando eso
// pasa.
//
// Se usa solo desde ficheros _test.go. Vive en pkg porque lo necesitan varios
// servicios y duplicarlo dejaria que las copias se separaran.
package sqlsource

import (
	"fmt"
	"os"
	"strings"
)

// Leer devuelve el contenido de un fichero fuente relativo al paquete que
// llama.
func Leer(nombre string) (string, error) {
	datos, err := os.ReadFile(nombre)
	if err != nil {
		return "", fmt.Errorf("no se pudo leer %s: %w", nombre, err)
	}
	return string(datos), nil
}

// ExtraerFuncion devuelve el cuerpo desde la firma hasta la llave de cierre a
// nivel cero. Devuelve "" si no encuentra la firma, para que quien llama pueda
// distinguir "la funcion cambio de nombre" de "la condicion desaparecio" -- son
// dos fallos distintos y el segundo es el peligroso.
func ExtraerFuncion(fuente, firma string) string {
	i := strings.Index(fuente, firma)
	if i < 0 {
		return ""
	}
	resto := fuente[i:]
	nivel := 0
	for j := 0; j < len(resto); j++ {
		switch resto[j] {
		case '{':
			nivel++
		case '}':
			nivel--
			if nivel == 0 {
				return resto[:j+1]
			}
		}
	}
	return resto
}
