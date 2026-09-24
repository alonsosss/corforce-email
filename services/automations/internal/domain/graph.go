package domain

import (
	"regexp"
	"strconv"
)

// El flujo es un grafo dirigido sin ciclos: el primer paso es la entrada, cada paso sigue
// en Next y una rama en Then o en Else. Un destino vacio es el fin del recorrido. Se valida
// entero en el dominio: ids unicos, destinos que existen, todo alcanzable desde la entrada,
// sin ciclos, ningun recorrido de mas de MaxDepth pasos, y las ramas que miran un correo
// solo lo hacen sobre un envio por el que pasa TODO recorrido que llega a ellas.
//
// Un flujo guardado antes de las ramas es una lista sin ids: UpgradeLegacySteps le da ids
// (s1, s2...) y encadena cada paso con el siguiente. La ejecucion sigue apuntando al paso
// por su posicion en la lista (runs.step_index), asi que las ejecuciones en curso de esos
// flujos no cambian.

// MaxStepIDLen acota el id de un paso.
const MaxStepIDLen = 32

var stepIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func stepField(i int) string { return "steps[" + strconv.Itoa(i) + "]" }

// UpgradeLegacySteps da ids y enlaces a una lista sin ninguno (flujo lineal anterior a las
// ramas). Una lista que ya tiene algun id o enlace no se toca.
func UpgradeLegacySteps(steps []Step) {
	for _, s := range steps {
		if s.ID != "" || s.Next != "" || s.Then != "" || s.Else != "" {
			return
		}
	}
	for i := range steps {
		steps[i].ID = "s" + strconv.Itoa(i+1)
	}
	for i := range steps {
		if i+1 < len(steps) && steps[i].Type != StepBranch {
			steps[i].Next = steps[i+1].ID
		}
	}
}

// targets son los destinos de un paso, sin los vacios.
func (s Step) targets() []string {
	var out []string
	for _, t := range []string{s.Next, s.Then, s.Else} {
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ValidateSteps normaliza y valida el flujo entero. Los mensajes nombran el paso.
func ValidateSteps(steps []Step) error {
	if len(steps) < MinSteps || len(steps) > MaxSteps {
		return NewValidationError("steps debe tener entre %d y %d pasos", MinSteps, MaxSteps)
	}
	UpgradeLegacySteps(steps)
	for i := range steps {
		if err := steps[i].normalize(i); err != nil {
			return err
		}
	}
	index := make(map[string]int, len(steps))
	for i, s := range steps {
		if !stepIDPattern.MatchString(s.ID) {
			return NewValidationError("%s.id es obligatorio: minusculas, digitos, _ y -, hasta %d", stepField(i), MaxStepIDLen)
		}
		if _, dup := index[s.ID]; dup {
			return NewValidationError("%s.id repite %q", stepField(i), s.ID)
		}
		index[s.ID] = i
	}
	adj := make([][]int, len(steps))
	for i, s := range steps {
		for _, t := range s.targets() {
			j, ok := index[t]
			if !ok {
				return NewValidationError("%s apunta a un paso que no existe: %q", stepField(i), t)
			}
			if j == i {
				return NewValidationError("%s apunta a si mismo", stepField(i))
			}
			adj[i] = append(adj[i], j)
		}
		if s.Type == StepBranch && s.Then != "" && s.Then == s.Else {
			return NewValidationError("%s: then y else van al mismo paso; la rama no decide nada", stepField(i))
		}
	}
	if i := firstInCycle(adj); i >= 0 {
		return NewValidationError("%s forma parte de un ciclo: un flujo no puede volver a un paso ya recorrido", stepField(i))
	}
	reach := reachable(adj, -1)
	for i := range steps {
		if !reach[i] {
			return NewValidationError("%s no es alcanzable desde el primer paso", stepField(i))
		}
	}
	if d := longestPath(adj); d > MaxDepth {
		return NewValidationError("un recorrido del flujo tiene %d pasos; el maximo es %d", d, MaxDepth)
	}
	for i, s := range steps {
		if s.Type != StepBranch || !s.Condition.Kind.ReferencesStep() {
			continue
		}
		j, ok := index[s.Condition.Step]
		if !ok {
			return NewValidationError("%s.condition.step no existe: %q", stepField(i), s.Condition.Step)
		}
		if steps[j].Type != StepSendEmail {
			return NewValidationError("%s.condition.step debe ser un paso send_email", stepField(i))
		}
		if !dominates(adj, j, i) {
			return NewValidationError("%s.condition.step debe ser un envio por el que pase todo recorrido que llega a la rama", stepField(i))
		}
	}
	return nil
}

// reachable marca lo alcanzable desde la entrada (0) sin pasar por skip (-1 = ninguno).
func reachable(adj [][]int, skip int) []bool {
	seen := make([]bool, len(adj))
	if len(adj) == 0 || skip == 0 {
		return seen
	}
	stack := []int{0}
	seen[0] = true
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, m := range adj[n] {
			if m != skip && !seen[m] {
				seen[m] = true
				stack = append(stack, m)
			}
		}
	}
	return seen
}

// dominates: todo recorrido desde la entrada hasta b pasa por d. Sin d, b deja de ser
// alcanzable.
func dominates(adj [][]int, d, b int) bool {
	if d == b {
		return true
	}
	return !reachable(adj, d)[b]
}

// firstInCycle devuelve un paso de algun ciclo o -1.
func firstInCycle(adj [][]int) int {
	const (
		white = iota
		grey
		black
	)
	color := make([]int, len(adj))
	var visit func(n int) int
	visit = func(n int) int {
		color[n] = grey
		for _, m := range adj[n] {
			switch color[m] {
			case grey:
				return m
			case white:
				if c := visit(m); c >= 0 {
					return c
				}
			}
		}
		color[n] = black
		return -1
	}
	for n := range adj {
		if color[n] == white {
			if c := visit(n); c >= 0 {
				return c
			}
		}
	}
	return -1
}

// longestPath es el maximo de pasos de un recorrido desde la entrada (grafo sin ciclos).
func longestPath(adj [][]int) int {
	memo := make([]int, len(adj))
	var depth func(n int) int
	depth = func(n int) int {
		if memo[n] > 0 {
			return memo[n]
		}
		best := 0
		for _, m := range adj[n] {
			if d := depth(m); d > best {
				best = d
			}
		}
		memo[n] = best + 1
		return memo[n]
	}
	if len(adj) == 0 {
		return 0
	}
	return depth(0)
}

// StepIndex es la posicion del paso con ese id, o -1.
func StepIndex(steps []Step, id string) int {
	for i, s := range steps {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// NextIndex es la posicion del paso que sigue al de la posicion i: el de Next, o en una
// rama el de Then si taken y el de Else si no. len(steps) es el fin del recorrido.
func NextIndex(steps []Step, i int, taken bool) int {
	if i < 0 || i >= len(steps) {
		return len(steps)
	}
	s := steps[i]
	if s.ID == "" && s.Next == "" && s.Then == "" && s.Else == "" {
		// Paso de una lista anterior a las ramas que nadie paso por UpgradeLegacySteps.
		return i + 1
	}
	target := s.Next
	if s.Type == StepBranch {
		target = s.Else
		if taken {
			target = s.Then
		}
	}
	if target == "" {
		return len(steps)
	}
	if j := StepIndex(steps, target); j >= 0 {
		return j
	}
	return len(steps)
}
