package mapping

import "testing"

func TestDirectMapper(t *testing.T) {
	mapper, err := NewPathMapper(Config{Mode: "direct"})
	if err != nil {
		t.Fatalf("expected mapper to build, got error: %v", err)
	}

	primary, shadow, err := mapper.Map("/api/v1/test")
	if err != nil {
		t.Fatalf("expected mapping to succeed, got error: %v", err)
	}
	if primary != "/api/v1/test" || shadow != "/api/v1/test" {
		t.Fatalf("expected direct mapping, got primary=%q shadow=%q", primary, shadow)
	}
}

func TestRegexMapper(t *testing.T) {
	mapper, err := NewPathMapper(Config{
		Mode: "rewrite",
		Rules: []Rule{
			{Match: "^/api/(.*)$", Primary: "/v1/$1", Shadow: "/legacy/$1"},
		},
	})
	if err != nil {
		t.Fatalf("expected mapper to build, got error: %v", err)
	}

	primary, shadow, err := mapper.Map("/api/users")
	if err != nil {
		t.Fatalf("expected mapping to succeed, got error: %v", err)
	}
	if primary != "/v1/users" {
		t.Fatalf("expected primary path to be rewritten, got %q", primary)
	}
	if shadow != "/legacy/users" {
		t.Fatalf("expected shadow path to be rewritten, got %q", shadow)
	}
}

func TestRegexMapperInvalidRule(t *testing.T) {
	_, err := NewPathMapper(Config{
		Mode:  "rewrite",
		Rules: []Rule{{Match: "["}},
	})
	if err == nil {
		t.Fatalf("expected invalid regex to fail")
	}
}
