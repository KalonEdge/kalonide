package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:11434":       "http://localhost:11434/v1",
		"http://localhost:11434/":      "http://localhost:11434/v1",
		"http://localhost:11434/v1/":   "http://localhost:11434/v1",
		"localhost:1234":               "http://localhost:1234/v1",
		"https://openrouter.ai/api/v1": "https://openrouter.ai/api/v1",
		"":                             DefaultKalonURL,
	} {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultsAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KALONIDE_HOME", dir)
	s, err := Load()
	if err != nil || s.Provider != "kalon" || s.Providers["kalon"].BaseURL != DefaultKalonURL || s.PermissionMode != ModeAsk {
		t.Fatalf("%+v %v", s, err)
	}
	s.Providers["or"] = Provider{BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "OR_KEY", Model: "x"}
	s.Provider = "or"
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(Path()); st.Mode().Perm() != 0o600 {
		t.Fatalf("settings perms %v", st.Mode())
	}
	t.Setenv("OR_KEY", "secret")
	t.Setenv("KALONIDE_MODEL", "override")
	s2, _ := Load()
	r, err := s2.Resolve("")
	if err != nil || r.Name != "or" || r.APIKey != "secret" || r.Model != "override" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestBrokenSettingsReportsError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KALONIDE_HOME", dir)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{oops"), 0o600)
	if _, err := Load(); err == nil {
		t.Fatal("broken JSON accepted silently")
	}
}

func TestImportsLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KALONIDE_HOME", dir)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"activeProvider":"kalon","providers":[
		{"name":"kalon","baseURL":"http://localhost:11434/v1","model":"gemma4:4b"},
		{"name":"openrouter","catalogID":"openrouter","baseURL":"https://openrouter.ai/api/v1","model":"a/b","apiKeyStored":true}]}`), 0o600)
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Providers["kalon"].Model != "gemma4:4b" || s.Providers["openrouter"].APIKeyEnv != "OPENROUTER_API_KEY" || s.Provider != "kalon" {
		t.Fatalf("%+v", s)
	}
}

func TestTrustedFolders(t *testing.T) {
	var s Settings
	s.SetTrust("/w/app", true)
	if !s.Trusts("/w/app") || !s.Trusts("/w/app/sub") || s.Trusts("/w/application") || s.Trusts("/w") {
		t.Fatal("trust matching")
	}
	s.SetTrust("/w/app", true)
	if len(s.TrustedFolders) != 1 {
		t.Fatal("duplicate entry")
	}
	s.SetTrust("/w/app", false)
	if s.Trusts("/w/app") {
		t.Fatal("untrust failed")
	}
}
