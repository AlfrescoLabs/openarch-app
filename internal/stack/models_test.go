package stack

import "testing"

func TestValidateModelURL(t *testing.T) {
	for _, ok := range []string{"http://localhost:11434", "http://localhost:11434/", "https://models.example.com", "http://192.168.1.10:8080"} {
		if err := ValidateModelURL(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"localhost:11434", "ftp://host", "http://", "http://localhost:11434/v1", "http://localhost:12434/engines/v1", "http://h?x=1"} {
		if err := ValidateModelURL(bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestContainerModelURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://localhost:11434":     "http://host.docker.internal:11434",
		"http://127.0.0.1:8080/":     "http://host.docker.internal:8080",
		"http://[::1]:11434":         "http://host.docker.internal:11434",
		"http://localhost":           "http://host.docker.internal",
		"http://192.168.1.10:11434":  "http://192.168.1.10:11434",
		"https://models.example.com": "https://models.example.com",
	} {
		if got := ContainerModelURL(in); got != want {
			t.Errorf("ContainerModelURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameModel(t *testing.T) {
	for _, c := range []struct {
		listed, model string
		want          bool
	}{
		{"docker.io/ai/qwen2.5:latest", "ai/qwen2.5", true},
		{"mxbai-embed-large:latest", "mxbai-embed-large", true},
		{"qwen2.5:7b", "qwen2.5:7b", true},
		{"qwen2.5:7b", "qwen2.5", false},
		{"nomic-embed-text:latest", "mxbai-embed-large", false},
	} {
		if got := SameModel(c.listed, c.model); got != c.want {
			t.Errorf("SameModel(%q, %q) = %v", c.listed, c.model, got)
		}
	}
}
