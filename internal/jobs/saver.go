package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Saver writes frame copies ("Shots", see CONTEXT.md) to disk.
type Saver struct {
	Dir string
}

// NewSaver builds a saver into dir; dir must be non-empty (checked by config).
func NewSaver(dir string) *Saver {
	return &Saver{Dir: dir}
}

// Save writes the frame as <timestamp>_<hash8>.<ext> and returns the path.
func (s *Saver) Save(img []byte, mime, hash string) (string, error) {
	if s == nil || s.Dir == "" {
		return "", nil
	}
	ext := ".jpg"
	if strings.Contains(mime, "png") {
		ext = ".png"
	}
	name := time.Now().Format("20060102-150405.000") + "_" + hash[:8] + ext
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return "", fmt.Errorf("create shots dir: %w", err)
	}
	path := filepath.Join(s.Dir, name)
	if err := os.WriteFile(path, img, 0o644); err != nil {
		return "", fmt.Errorf("write shot: %w", err)
	}
	return path, nil
}
