package main

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const maxLineBytes = 64 << 10

var (
	folderRe    = regexp.MustCompile(`^Folder\s+(\d+)/(\d+) \[(.*?)\]\s+-> \[`)
	selectedRe  = regexp.MustCompile(`^Host1: folder \[.*\] selected (\d+) messages`)
	copiedRe    = regexp.MustCompile(`^msg .* \{(\d+)\}\s+copied to `)
	failedRe    = regexp.MustCompile(`^- msg .* could not `)
	pipemessRe  = regexp.MustCompile(`^Failure: --pipemess command .* exit value "(\d+)"`)
	listingRe   = regexp.MustCompile(`^\+\+\+\+ Listing \d+ errors`)
	endLoopRe   = regexp.MustCompile(`^\+\+\+\+ End looping on each folder`)
	nbMessages  = regexp.MustCompile(`^Host1 Nb messages:\s+(\d+) messages`)
	nbFolders   = regexp.MustCompile(`^Host1 Nb folders:\s+(\d+) folders`)
	notInDestRe = regexp.MustCompile(`^Messages found in host1 not in host2\s+:\s+(\d+)`)
	detectedRe  = regexp.MustCompile(`^Detected (\d+) errors`)
	exitRe      = regexp.MustCompile(`^Exiting with return value (\d+)`)
	sideFailRe  = regexp.MustCompile(`^(?:Err \d+/\d+: )?Host([12]) failure: (.*)$`)
)

// outputParser lee la salida de imapsync linea a linea y guarda solo contadores y banderas: la
// salida completa lleva nombres de carpeta, usuarios y asuntos, y ni se guarda ni se registra. Es un
// io.Writer para conectarlo directamente al proceso; el largo de linea esta acotado.
type outputParser struct {
	mu   sync.Mutex
	buf  []byte
	skip bool

	foldersTotal  int
	messagesTotal int
	haveTotals    bool
	folders       []*folderState
	current       *folderState
	inErrorList   bool

	copied int
	failed int
	bytes  int64

	scanInfected    int
	scanUnavailable int
	scanTooBig      int

	exitCode       int
	haveExit       bool
	detectedErrors int
	notInDest      int
	overQuota      bool
	sourceTLS      bool
	destTLS        bool
}

type folderState struct {
	name     string
	selected int
	copied   int
	failed   int
	done     bool
}

func newOutputParser() *outputParser { return &outputParser{} }

func (p *outputParser) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rest := data
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			if !p.skip {
				if len(p.buf)+len(rest) > maxLineBytes {
					p.skip = true
					p.buf = p.buf[:0]
				} else {
					p.buf = append(p.buf, rest...)
				}
			}
			break
		}
		if !p.skip {
			if len(p.buf)+i <= maxLineBytes {
				p.buf = append(p.buf, rest[:i]...)
				p.line(strings.TrimRight(string(p.buf), "\r"))
			}
		}
		p.buf = p.buf[:0]
		p.skip = false
		rest = rest[i+1:]
	}
	return len(data), nil
}

func (p *outputParser) line(l string) {
	switch {
	case p.inErrorList:
		p.summaryLine(l)
		return
	case listingRe.MatchString(l):
		p.inErrorList = true
		p.finishFolder()
		return
	}
	if m := folderRe.FindStringSubmatch(l); m != nil {
		p.finishFolder()
		p.foldersTotal = atoiBounded(m[2])
		p.current = &folderState{name: truncateRunes(sanitizeName(m[3]), maxFolderName)}
		p.folders = append(p.folders, p.current)
		return
	}
	if m := selectedRe.FindStringSubmatch(l); m != nil && p.current != nil {
		p.current.selected = atoiBounded(m[1])
		return
	}
	if m := copiedRe.FindStringSubmatch(l); m != nil {
		p.copied++
		p.bytes += int64(atoiBounded(m[1]))
		if p.current != nil {
			p.current.copied++
		}
		return
	}
	if failedRe.MatchString(l) {
		p.failed++
		if p.current != nil {
			p.current.failed++
		}
		if strings.Contains(l, "[OVERQUOTA]") {
			p.overQuota = true
		}
		return
	}
	if m := pipemessRe.FindStringSubmatch(l); m != nil {
		switch atoiBounded(m[1]) {
		case scanExitInfected:
			p.scanInfected++
		case scanExitTooBig:
			p.scanTooBig++
		default:
			p.scanUnavailable++
		}
		return
	}
	if endLoopRe.MatchString(l) {
		p.finishFolder()
		return
	}
	if m := nbMessages.FindStringSubmatch(l); m != nil && !p.haveTotals {
		p.messagesTotal = atoiBounded(m[1])
		p.haveTotals = true
		return
	}
	if m := nbFolders.FindStringSubmatch(l); m != nil && p.foldersTotal == 0 {
		p.foldersTotal = atoiBounded(m[1])
		return
	}
	p.summaryLine(l)
}

// summaryLine atiende lo que imapsync imprime fuera del bucle de carpetas: el resumen, el codigo de
// salida y, tras el listado de errores, la repeticion de los de cada mensaje, que no se cuenta otra vez.
func (p *outputParser) summaryLine(l string) {
	if m := exitRe.FindStringSubmatch(l); m != nil {
		p.exitCode, p.haveExit = atoiBounded(m[1]), true
		return
	}
	if m := detectedRe.FindStringSubmatch(l); m != nil {
		p.detectedErrors = atoiBounded(m[1])
		return
	}
	if m := notInDestRe.FindStringSubmatch(l); m != nil {
		p.notInDest = atoiBounded(m[1])
		return
	}
	if m := sideFailRe.FindStringSubmatch(l); m != nil && isTLSFailure(m[2]) {
		if m[1] == "1" {
			p.sourceTLS = true
		} else {
			p.destTLS = true
		}
	}
}

func isTLSFailure(text string) bool {
	for _, marker := range []string{"verification failed", "certificate verify failed", "SSL connect attempt failed", "Can not go to tls encryption", "Unable to start TLS"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (p *outputParser) finishFolder() {
	if p.current != nil {
		p.current.done = true
		p.current = nil
	}
}

// Progress es la foto actual de la pasada: lo que se envia en cada latido y al cerrar.
func (p *outputParser) Progress() Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := Progress{
		FoldersTotal:   p.foldersTotal,
		MessagesTotal:  p.messagesTotal,
		MessagesCopied: p.copied,
		MessagesFailed: p.failed,
		BytesCopied:    p.bytes,
		Folders:        make([]FolderProgress, 0, len(p.folders)),
	}
	for _, f := range p.folders {
		skipped := max(0, f.selected-f.copied-f.failed)
		if f.done {
			out.FoldersDone++
			out.MessagesSkipped += skipped
		}
		if len(out.Folders) < maxFolders {
			out.Folders = append(out.Folders, FolderProgress{Name: f.name, MessagesCopied: f.copied, MessagesSkipped: skipped, MessagesFailed: f.failed})
		}
	}
	if out.FoldersTotal < out.FoldersDone {
		out.FoldersTotal = out.FoldersDone
	}
	return out
}

// Findings son las banderas que decide la clasificacion de una pasada.
type Findings struct {
	Exit            int
	HaveExit        bool
	DetectedErrors  int
	NotInDest       int
	Failed          int
	ScanInfected    int
	ScanUnavailable int
	ScanTooBig      int
	OverQuota       bool
	SourceTLS       bool
	DestTLS         bool
}

func (p *outputParser) Findings() Findings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Findings{
		Exit: p.exitCode, HaveExit: p.haveExit, DetectedErrors: p.detectedErrors, NotInDest: p.notInDest, Failed: p.failed,
		ScanInfected: p.scanInfected, ScanUnavailable: p.scanUnavailable, ScanTooBig: p.scanTooBig,
		OverQuota: p.overQuota, SourceTLS: p.sourceTLS, DestTLS: p.destTLS,
	}
}

// ScanUnavailableCount permite al ejecutor cortar la pasada cuando el antivirus cae a mitad: sin
// esto imapsync seguiria recorriendo el buzon entero rechazando cada mensaje.
func (p *outputParser) ScanUnavailableCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scanUnavailable
}

func atoiBounded(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 1<<31-1 {
		return 0
	}
	return n
}
