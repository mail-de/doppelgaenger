package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

const (
	configFlag     = "--config"
	versionFlag    = "--version"
	testConfigPath = "/tmp/config.yaml"
)

func TestParseCLIAcceptsLongOptions(t *testing.T) {
	opts, err := parseCLI([]string{versionFlag, configFlag, testConfigPath}, io.Discard)
	if err != nil {
		t.Fatalf("parseCLI returned error: %v", err)
	}

	if !opts.version {
		t.Fatalf("expected version to be true")
	}

	if opts.configPath != testConfigPath {
		t.Fatalf("expected config path to be parsed, got %q", opts.configPath)
	}
}

func TestParseCLIAcceptsShortOptions(t *testing.T) {
	opts, err := parseCLI([]string{"-h", "-c", testConfigPath}, io.Discard)
	if err != nil {
		t.Fatalf("parseCLI returned error: %v", err)
	}

	if !opts.help {
		t.Fatalf("expected help to be true")
	}

	if opts.configPath != testConfigPath {
		t.Fatalf("expected config path to be parsed, got %q", opts.configPath)
	}
}

func TestParseCLIRejectsUnexpectedArgs(t *testing.T) {
	_, err := parseCLI([]string{"unexpected"}, io.Discard)
	if err == nil {
		t.Fatalf("expected unexpected argument to fail")
	}
}

func TestParseCLIRejectsSingleDashLongOption(t *testing.T) {
	_, err := parseCLI([]string{"-config", testConfigPath}, io.Discard)
	if err == nil {
		t.Fatalf("expected single-dash long option to fail")
	}
}

func TestPrintVersion(t *testing.T) {
	var buf bytes.Buffer

	printVersion(&buf, "v1.2.3")

	if got := buf.String(); got != "doppelgaenger v1.2.3\n" {
		t.Fatalf("unexpected version output: %q", got)
	}
}

func TestPrintUsageMentionsRuntimeFlags(t *testing.T) {
	var buf bytes.Buffer

	printUsage(&buf, "doppelgaenger")

	output := buf.String()
	for _, want := range []string{versionFlag, configFlag, "--help"} {
		if !strings.Contains(output, want) {
			t.Fatalf("usage output must mention %s: %s", want, output)
		}
	}
}
