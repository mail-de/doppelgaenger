package mapping

import "testing"

const apiV1TestPath = "/api/v1/test"

func TestDirectMapper(t *testing.T) {
	mapper, err := NewPathMapper(Config{Mode: modeDirect})
	if err != nil {
		t.Fatalf("expected mapper to build, got error: %v", err)
	}

	primary, shadow, err := mapper.Map(apiV1TestPath)
	if err != nil {
		t.Fatalf("expected mapping to succeed, got error: %v", err)
	}

	if primary != apiV1TestPath || shadow != apiV1TestPath {
		t.Fatalf("expected direct mapping, got primary=%q shadow=%q", primary, shadow)
	}
}

func TestRegexMapper(t *testing.T) {
	mapper, err := NewPathMapper(Config{
		Mode: modeRewrite,
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
		Mode:  modeRewrite,
		Rules: []Rule{{Match: "["}},
	})
	if err == nil {
		t.Fatalf("expected invalid regex to fail")
	}
}
