package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve turns a model-supplied path into an absolute path and checks that
// it lies inside the workspace or an extra root. Symlinks are resolved on
// the existing part of the path, so a link cannot point outside.
func (e *Env) Resolve(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("path is required")
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.Root, p)
	}
	p = filepath.Clean(p)
	real, err := evalExisting(p)
	if err != nil {
		return "", err
	}
	for _, root := range e.roots() {
		if within(root, real) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s is outside the workspace (%s); the user can allow it with /add-dir or --add-dir", p, e.Root)
}

// Rel returns p relative to the workspace when it is inside it.
func (e *Env) Rel(p string) string {
	if r, err := filepath.Rel(e.Root, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return p
}

func (e *Env) roots() []string {
	out := []string{e.Root}
	out = append(out, e.ExtraRoots...)
	return out
}

// RealPath resolves symlinks in an absolute directory path, for roots.
func RealPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return real, nil
}

// evalExisting resolves symlinks in the longest existing prefix of p and
// appends the rest unchanged.
func evalExisting(p string) (string, error) {
	rest := ""
	cur := p
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func within(root, p string) bool {
	if root == p {
		return true
	}
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// skipDirs are never descended into by listing and search tools.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".build": true, "dist": true, "build": true,
	"target": true, "vendor": true, "__pycache__": true, ".venv": true, "venv": true,
	".next": true, ".cache": true, ".idea": true, ".gradle": true, "DerivedData": true,
	".tox": true, ".mypy_cache": true, ".pytest_cache": true, "coverage": true,
}
