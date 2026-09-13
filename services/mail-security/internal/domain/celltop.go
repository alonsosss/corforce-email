package domain

import "sort"

// CellQuarantineTop es lo que se escribe en las claves Q_* de Redis. Esas claves son
// unicas por celda (los motores copiados las leen como valores globales) mientras que los
// ajustes son por empresa: Redis recibe el TOPE (el maximo de cada limite y la union de
// dominios excluidos) y la aplicacion por empresa la hace /pipe con la fila de la empresa.
type CellQuarantineTop struct {
	MaxSizeMiB     int64
	MaxAgeDays     int
	RetentionSize  int
	ExcludeDomains []string
}

// ComputeCellQuarantineTop combina los ajustes de todas las empresas. Sin filas, rigen
// los valores por defecto.
func ComputeCellQuarantineTop(all []QuarantineSettings) CellQuarantineTop {
	top := CellQuarantineTop{
		MaxSizeMiB:     bytesToMiB(DefaultQuarantineMaxSizeBytes),
		MaxAgeDays:     DefaultQuarantineMaxAgeDays,
		RetentionSize:  DefaultQuarantineRetentionSize,
		ExcludeDomains: []string{},
	}
	seen := map[string]struct{}{}
	for _, s := range all {
		if mib := bytesToMiB(s.MaxSizeBytes); mib > top.MaxSizeMiB {
			top.MaxSizeMiB = mib
		}
		if s.MaxAgeDays > top.MaxAgeDays {
			top.MaxAgeDays = s.MaxAgeDays
		}
		if s.RetentionSize > top.RetentionSize {
			top.RetentionSize = s.RetentionSize
		}
		for _, d := range s.ExcludeDomains {
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			top.ExcludeDomains = append(top.ExcludeDomains, d)
		}
	}
	sort.Strings(top.ExcludeDomains)
	return top
}

// bytesToMiB redondea hacia arriba: un tope en MiB nunca debe quedar por debajo del
// limite en bytes de ninguna empresa.
func bytesToMiB(b int64) int64 {
	const mib = 1024 * 1024
	if b <= 0 {
		return 0
	}
	return (b + mib - 1) / mib
}
