package llm

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// MaxImageBytes is the largest image Kalonide will send.
const MaxImageBytes = 8 << 20

// LoadImage reads an image file and returns it as a data URL. The type is
// detected from the file's contents, not its name.
func LoadImage(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a folder", path)
	}
	if info.Size() > MaxImageBytes {
		return "", fmt.Errorf("%s is %d MB; images must be under %d MB", path, info.Size()>>20, MaxImageBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mime := http.DetectContentType(data)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return "", fmt.Errorf("%s is not a PNG, JPEG, GIF or WebP image (looks like %s)", path, strings.Split(mime, ";")[0])
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}
