// Package config loads and saves Kalonide settings.
//
// Settings live in one JSON file, settings.json, inside the Kalonide home
// directory. API keys are never stored: a provider names the environment
// variable that holds its key.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/KalonEdge/kalonide/internal/mcp"
)

// DefaultProvider is the provider used when nothing else is configured: the
// Kalon desktop app's local OpenAI-compatible endpoint.
const DefaultProvider = "kalon"

// DefaultKalonURL is where the Kalon app serves its API by default.
const DefaultKalonURL = "http://localhost:11434/v1"

// Permission modes.
const (
	ModeAsk      = "ask"       // ask before writes, shell and network
	ModeAutoEdit = "auto-edit" // file edits run without asking; shell and network ask
	ModeYolo     = "yolo"      // everything runs without asking, except destructive commands
)

// Provider is one model endpoint.
type Provider struct {
	BaseURL   string            `json:"baseURL"`
	Model     string            `json:"model,omitempty"`
	APIKeyEnv string            `json:"apiKeyEnv,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// Settings is the content of settings.json.
type Settings struct {
	Provider       string                      `json:"provider"`
	Providers      map[string]Provider         `json:"providers"`
	PermissionMode string                      `json:"permissionMode,omitempty"`
	MaxTurns       int                         `json:"maxTurns,omitempty"`
	ContextWindow  int                         `json:"contextWindow,omitempty"`
	FallbackModels []string                    `json:"fallbackModels,omitempty"`
	MCPServers     map[string]mcp.ServerConfig `json:"mcpServers,omitempty"`
}

// Defaults returns settings that work with a stock Kalon install.
func Defaults() Settings {
	return Settings{
		Provider: DefaultProvider,
		Providers: map[string]Provider{
			DefaultProvider: {BaseURL: DefaultKalonURL},
		},
		PermissionMode: ModeAsk,
		MaxTurns:       60,
		ContextWindow:  32768,
	}
}

// Home returns the Kalonide home directory: $KALONIDE_HOME, else
// $XDG_CONFIG_HOME/kalonide, else ~/.config/kalonide.
func Home() string {
	if h := os.Getenv("KALONIDE_HOME"); h != "" {
		return h
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "kalonide")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".kalonide"
	}
	return filepath.Join(home, ".config", "kalonide")
}

// Path returns the settings file path.
func Path() string { return filepath.Join(Home(), "settings.json") }

// SessionsDir returns where sessions are stored.
func SessionsDir() string { return filepath.Join(Home(), "sessions") }

// UserMemoryDir returns the user-scope memory directory.
func UserMemoryDir() string { return filepath.Join(Home(), "memory") }

// HistoryPath returns the interactive input history file.
func HistoryPath() string { return filepath.Join(Home(), "history") }

// Load reads settings.json. A missing file yields defaults (seeded from a
// legacy config.json when one exists). Environment overrides are applied
// on top but never written back.
func Load() (Settings, error) {
	s, err := loadFile(Path())
	if err != nil {
		return s, err
	}
	s.normalize()
	return s, nil
}

func loadFile(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s := Defaults()
		importLegacy(&s, filepath.Join(filepath.Dir(path), "config.json"))
		return s, nil
	}
	if err != nil {
		return Defaults(), err
	}
	s := Defaults()
	s.Providers = nil
	if err := json.Unmarshal(data, &s); err != nil {
		return Defaults(), fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return s, nil
}

// normalize fills gaps so callers never see a half-configured value.
func (s *Settings) normalize() {
	if s.Providers == nil {
		s.Providers = map[string]Provider{}
	}
	if len(s.Providers) == 0 {
		s.Providers[DefaultProvider] = Provider{BaseURL: DefaultKalonURL}
	}
	if s.Provider == "" {
		s.Provider = DefaultProvider
	}
	if _, ok := s.Providers[s.Provider]; !ok {
		names := s.ProviderNames()
		s.Provider = names[0]
	}
	switch s.PermissionMode {
	case ModeAsk, ModeAutoEdit, ModeYolo:
	default:
		s.PermissionMode = ModeAsk
	}
	if s.MaxTurns <= 0 {
		s.MaxTurns = 60
	}
	if s.ContextWindow < 2048 {
		s.ContextWindow = 32768
	}
}

// ProviderNames returns provider names in sorted order.
func (s *Settings) ProviderNames() []string {
	names := make([]string, 0, len(s.Providers))
	for n := range s.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Save writes settings.json atomically with owner-only permissions.
func Save(s Settings) error {
	return saveFile(Path(), s)
}

func saveFile(path string, s Settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, append(data, '\n'), 0o600)
}

// WriteFileAtomic writes data to a temp file beside path, then renames it
// into place, so readers never see a partial file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Resolved is the effective provider after environment overrides.
type Resolved struct {
	Name      string
	BaseURL   string
	Model     string
	APIKey    string
	APIKeyEnv string
	Headers   map[string]string
}

// Resolve picks the provider to use. name may be empty for the active one.
// Environment overrides: KALONIDE_PROVIDER, KALONIDE_BASE_URL,
// KALONIDE_MODEL, KALONIDE_API_KEY.
func (s *Settings) Resolve(name string) (Resolved, error) {
	if name == "" {
		name = os.Getenv("KALONIDE_PROVIDER")
	}
	if name == "" {
		name = s.Provider
	}
	p, ok := s.Providers[name]
	if !ok {
		return Resolved{}, fmt.Errorf("no provider named %q (have: %s)", name, strings.Join(s.ProviderNames(), ", "))
	}
	r := Resolved{Name: name, BaseURL: p.BaseURL, Model: p.Model, APIKeyEnv: p.APIKeyEnv, Headers: p.Headers}
	if v := os.Getenv("KALONIDE_BASE_URL"); v != "" {
		r.BaseURL = v
	}
	if v := os.Getenv("KALONIDE_MODEL"); v != "" {
		r.Model = v
	}
	if p.APIKeyEnv != "" {
		r.APIKey = os.Getenv(p.APIKeyEnv)
	}
	if v := os.Getenv("KALONIDE_API_KEY"); v != "" {
		r.APIKey = v
	}
	r.BaseURL = NormalizeBaseURL(r.BaseURL)
	return r, nil
}

// NormalizeBaseURL trims trailing slashes and adds /v1 to a bare host URL,
// so "http://localhost:11434" and "http://localhost:11434/v1/" both work.
func NormalizeBaseURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return DefaultKalonURL
	}
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	u = strings.TrimRight(u, "/")
	rest := u[strings.Index(u, "://")+3:]
	if !strings.Contains(rest, "/") {
		u += "/v1"
	}
	return u
}

// EnvInt reads a positive integer environment variable.
func EnvInt(name string) (int, bool) {
	v := os.Getenv(name)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// legacyConfig is the shape of the previous CLI's config.json. Only names,
// URLs and models are imported; keys were never stored in plain text there
// either.
type legacyConfig struct {
	ActiveProvider string `json:"activeProvider"`
	Providers      []struct {
		Name    string `json:"name"`
		BaseURL string `json:"baseURL"`
		Model   string `json:"model"`
		Catalog string `json:"catalogID"`
	} `json:"providers"`
}

func importLegacy(s *Settings, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var lc legacyConfig
	if json.Unmarshal(data, &lc) != nil || len(lc.Providers) == 0 {
		return
	}
	for _, p := range lc.Providers {
		if p.Name == "" || p.BaseURL == "" {
			continue
		}
		np := Provider{BaseURL: p.BaseURL, Model: p.Model}
		switch {
		case strings.Contains(p.Catalog, "openrouter") || strings.Contains(p.BaseURL, "openrouter.ai"):
			np.APIKeyEnv = "OPENROUTER_API_KEY"
		case strings.Contains(p.BaseURL, "api.openai.com"):
			np.APIKeyEnv = "OPENAI_API_KEY"
		}
		s.Providers[p.Name] = np
	}
	if _, ok := s.Providers[lc.ActiveProvider]; ok {
		s.Provider = lc.ActiveProvider
	}
}
