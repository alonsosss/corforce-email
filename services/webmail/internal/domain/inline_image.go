package domain

import (
	"bytes"
	"strings"
)

// MaxInlineImages acota las imagenes insertadas en el cuerpo de un mensaje. Cada una viaja
// como una parte con Content-ID dentro de multipart/related.
const MaxInlineImages = 30

// InlineImage es una imagen insertada en el cuerpo HTML. Llega como data: y sale como una
// parte del mensaje a la que el HTML apunta con cid:, que es lo que muestran Gmail y
// Outlook (ambos bloquean las imagenes data:).
type InlineImage struct {
	ContentID   string
	ContentType string
	Data        []byte
}

// Filename es el nombre con el que la imagen aparece si el cliente la ofrece como fichero.
func (i InlineImage) Filename() string {
	ext := strings.TrimPrefix(i.ContentType, "image/")
	if ext == "jpeg" {
		ext = "jpg"
	}
	id := i.ContentID
	if at := strings.IndexByte(id, '@'); at > 0 {
		id = id[:at]
	}
	if len(id) > 12 {
		id = id[:12]
	}
	return "imagen-" + id + "." + ext
}

// SniffInlineImage confirma que los bytes son del tipo declarado. El tipo del data: lo
// escribe el cliente; lo que decide es la firma del fichero.
func SniffInlineImage(contentType string, data []byte) bool {
	switch contentType {
	case "image/png":
		return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
	case "image/jpeg":
		return bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF})
	case "image/gif":
		return bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a"))
	case "image/webp":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
	}
	return false
}
