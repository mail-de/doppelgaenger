package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadFailoverDefaultsAreDisabled(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: http
primary_base_urls:
  - "https://primary.example.com"
`))

	for name, failover := range map[string]FailoverConfig{
		"primary_failover": loaded.PrimaryFailover,
		"shadow_failover":  loaded.ShadowFailover,
	} {
		want := FailoverConfig{
			Enabled:           false,
			MaxTargets:        0,
			Retries:           0,
			BackoffInitial:    defaultFailoverBackoffInitial,
			BackoffMultiplier: defaultFailoverBackoffMultiplier,
			BackoffMax:        defaultFailoverBackoffMax,
			BackoffJitter:     defaultFailoverBackoffJitter,
			MaxWait:           defaultFailoverMaxWait,
			Cooldown:          defaultFailoverCooldown,
			AttemptTimeout:    defaultFailoverAttemptTimeout,
		}

		if failover != want {
			t.Fatalf("%s defaults = %+v, want %+v", name, failover, want)
		}
	}
}

func TestLoadFailoverConfig(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: grpc
shadow_sample_percent: 0
primary_grpc_targets:
  - address: "primary-a.example.com:9443"
  - address: "primary-b.example.com:9443"
primary_failover:
  enabled: true
  max_targets: 2
  retries: 3
  backoff_initial: "50ms"
  backoff_multiplier: 3
  backoff_max: "1s"
  backoff_jitter: 0
  max_wait: "4s"
  cooldown: "0s"
  attempt_timeout: "750ms"
shadow_failover:
  enabled: true
`))

	want := FailoverConfig{
		Enabled:           true,
		MaxTargets:        2,
		Retries:           3,
		BackoffInitial:    50 * time.Millisecond,
		BackoffMultiplier: 3,
		BackoffMax:        time.Second,
		BackoffJitter:     0,
		MaxWait:           4 * time.Second,
		Cooldown:          0,
		AttemptTimeout:    750 * time.Millisecond,
	}

	if loaded.PrimaryFailover != want {
		t.Fatalf("primary_failover = %+v, want %+v", loaded.PrimaryFailover, want)
	}

	if !loaded.ShadowFailover.Enabled || loaded.ShadowFailover.MaxWait != defaultFailoverMaxWait {
		t.Fatalf("shadow_failover not merged with defaults: %+v", loaded.ShadowFailover)
	}
}

func TestLoadFailoverRejectsInvalidValues(t *testing.T) {
	base := `
protocol: http
primary_base_urls:
  - "https://primary.example.com"
`

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "negative max targets", yaml: "primary_failover:\n  max_targets: -1\n", want: "primary_failover.max_targets"},
		{name: "negative retries", yaml: "primary_failover:\n  retries: -1\n", want: "primary_failover.retries"},
		{name: "too many retries", yaml: "shadow_failover:\n  retries: 21\n", want: "shadow_failover.retries"},
		{name: "zero backoff initial", yaml: "primary_failover:\n  backoff_initial: \"0s\"\n", want: "primary_failover.backoff_initial"},
		{name: "backoff max below initial", yaml: "primary_failover:\n  backoff_initial: \"1s\"\n  backoff_max: \"100ms\"\n", want: "primary_failover.backoff_max"},
		{name: "multiplier below one", yaml: "primary_failover:\n  backoff_multiplier: 0.5\n", want: "primary_failover.backoff_multiplier"},
		{name: "jitter above one", yaml: "primary_failover:\n  backoff_jitter: 1.5\n", want: "primary_failover.backoff_jitter"},
		{name: "negative max wait", yaml: "primary_failover:\n  max_wait: \"-1s\"\n", want: "primary_failover.max_wait"},
		{name: "negative cooldown", yaml: "shadow_failover:\n  cooldown: \"-1s\"\n", want: "shadow_failover.cooldown"},
		{name: "zero attempt timeout", yaml: "primary_failover:\n  attempt_timeout: \"0s\"\n", want: "primary_failover.attempt_timeout"},
		{name: "huge backoff max", yaml: "primary_failover:\n  backoff_max: \"2h\"\n", want: "primary_failover.durations"},
		{name: "huge cooldown", yaml: "shadow_failover:\n  cooldown: \"1000h\"\n", want: "shadow_failover.durations"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expectLoadErrorContains(t, []byte(base+tt.yaml), tt.want)
		})
	}
}

func TestLoadFailoverRejectsMilter(t *testing.T) {
	expectLoadErrorContains(t, []byte(`
protocol: milter
primary_failover:
  enabled: true
`), "primary_failover.enabled is not supported with protocol milter")

	expectLoadErrorContains(t, []byte(`
protocol: milter
shadow_failover:
  enabled: true
`), "shadow_failover.enabled is not supported with protocol milter")
}

func TestLoadFailoverDisabledIsAcceptedForMilter(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: milter
primary_failover:
  enabled: false
`))

	if loaded.PrimaryFailover.Enabled {
		t.Fatal("expected disabled primary failover")
	}
}

func expectLoadErrorContains(t *testing.T, configContent []byte, want string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, configContent, 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	t.Setenv("CONFIG_FILE", path)

	_, err := Load()
	if err == nil {
		t.Fatalf("expected load error containing %q", want)
	}

	if !strings.Contains(err.Error(), want) {
		t.Fatalf("load error %q does not contain %q", err, want)
	}
}

func TestLoadSelectionModeLeastConn(t *testing.T) {
	loaded := loadTestConfig(t, []byte(`
protocol: http
primary_base_urls:
  - "https://primary-a.example.com"
  - "https://primary-b.example.com"
primary_selection_mode: " Least_Conn "
shadow_selection_mode: "least_conn"
`))

	if loaded.PrimarySelectionMode != selectionLeastConn || loaded.ShadowSelectionMode != selectionLeastConn {
		t.Fatalf("selection modes = %q and %q, want least_conn", loaded.PrimarySelectionMode, loaded.ShadowSelectionMode)
	}

	loaded = loadTestConfig(t, []byte(`
protocol: grpc
shadow_sample_percent: 0
primary_grpc_targets:
  - address: "primary-a.example.com:9443"
  - address: "primary-b.example.com:9443"
primary_grpc_selection_mode: "LEAST_CONN"
shadow_grpc_selection_mode: "least_conn"
`))

	if loaded.PrimaryGRPCSelectionMode != selectionLeastConn || loaded.ShadowGRPCSelectionMode != selectionLeastConn {
		t.Fatalf("gRPC selection modes = %q and %q, want least_conn", loaded.PrimaryGRPCSelectionMode, loaded.ShadowGRPCSelectionMode)
	}
}

func TestLoadGRPCSelectionModeErrorNamesLeastConn(t *testing.T) {
	expectLoadErrorContains(t, []byte(`
protocol: grpc
shadow_sample_percent: 0
primary_grpc_targets:
  - address: "primary-a.example.com:9443"
primary_grpc_selection_mode: "leastconn"
`), "primary_grpc_selection_mode must be one of round_robin, source_ip_hash, or least_conn")
}

// least_conn depends on the cooldown: an unreachable backend has no request
// in flight and would otherwise be the first choice of every request.
func TestLoadLeastConnRequiresCooldown(t *testing.T) {
	const (
		httpBase = `
protocol: http
primary_base_urls:
  - "https://primary-a.example.com"
  - "https://primary-b.example.com"
`
		grpcBase = `
protocol: grpc
shadow_sample_percent: 0
primary_grpc_targets:
  - address: "primary-a.example.com:9443"
  - address: "primary-b.example.com:9443"
`
		primaryNoCooldown = "primary_failover:\n  cooldown: \"0s\"\n"
		shadowNoCooldown  = "shadow_failover:\n  cooldown: \"0s\"\n"
	)

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "http primary",
			yaml: httpBase + "primary_selection_mode: least_conn\n" + primaryNoCooldown,
			want: "primary_failover.cooldown must be > 0 when primary_selection_mode is least_conn",
		},
		{
			name: "http shadow",
			yaml: httpBase + "shadow_selection_mode: least_conn\n" + shadowNoCooldown,
			want: "shadow_failover.cooldown must be > 0 when shadow_selection_mode is least_conn",
		},
		{
			name: "grpc primary",
			yaml: grpcBase + "primary_grpc_selection_mode: least_conn\n" + primaryNoCooldown,
			want: "primary_failover.cooldown must be > 0 when primary_grpc_selection_mode is least_conn",
		},
		{
			name: "grpc shadow",
			yaml: grpcBase + "shadow_grpc_selection_mode: least_conn\n" + shadowNoCooldown,
			want: "shadow_failover.cooldown must be > 0 when shadow_grpc_selection_mode is least_conn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expectLoadErrorContains(t, []byte(tt.yaml), tt.want)
		})
	}

	// Only the selection mode of the active protocol counts, and other modes
	// keep working without a cooldown.
	loadTestConfig(t, []byte(grpcBase+"primary_selection_mode: least_conn\n"+primaryNoCooldown))
	loadTestConfig(t, []byte(httpBase+"primary_selection_mode: round_robin\n"+primaryNoCooldown))
	loadTestConfig(t, []byte(httpBase+"primary_selection_mode: least_conn\n"+shadowNoCooldown))
}
