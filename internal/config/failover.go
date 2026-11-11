package config

import (
	"fmt"
	"time"

	"github.com/spf13/viper"

	"doppelgaenger/internal/failover"
)

const (
	failoverPrimaryKey = "primary_failover"
	failoverShadowKey  = "shadow_failover"

	// maxFailoverRetries bounds extra rounds so a misconfiguration cannot
	// hold requests for an unbounded number of attempts.
	maxFailoverRetries = 20

	// maxFailoverDuration bounds every failover duration. It keeps backoff
	// arithmetic far away from time.Duration overflow.
	maxFailoverDuration = time.Hour

	defaultFailoverBackoffInitial    = 100 * time.Millisecond
	defaultFailoverBackoffMultiplier = 2.0
	defaultFailoverBackoffMax        = 2 * time.Second
	defaultFailoverBackoffJitter     = 0.2
	defaultFailoverMaxWait           = 5 * time.Second
	defaultFailoverCooldown          = 10 * time.Second
	defaultFailoverAttemptTimeout    = 2 * time.Second
)

// FailoverConfig configures connect-phase retry and failover for one backend
// class. Only failures where the request provably never reached the backend
// are repeated, so the setting is safe for non-idempotent requests.
// Applies to: HTTP and gRPC protocols.
type FailoverConfig struct {
	// Enabled turns retry and failover on. false keeps the single-attempt behavior.
	Enabled bool `mapstructure:"enabled"`
	// MaxTargets limits distinct targets per round. 0 means all, 1 means retry only.
	MaxTargets int `mapstructure:"max_targets"`
	// Retries is the number of extra rounds after the first one.
	Retries int `mapstructure:"retries"`
	// BackoffInitial is the delay before the first extra round.
	BackoffInitial time.Duration `mapstructure:"backoff_initial"`
	// BackoffMultiplier grows the delay exponentially per round.
	BackoffMultiplier float64 `mapstructure:"backoff_multiplier"`
	// BackoffMax caps a single backoff delay.
	BackoffMax time.Duration `mapstructure:"backoff_max"`
	// BackoffJitter randomizes each delay by +/- this fraction (0..1).
	BackoffJitter float64 `mapstructure:"backoff_jitter"`
	// MaxWait is the total budget after which no new attempt starts. 0 means
	// no budget beyond the attempt count and the request deadline.
	MaxWait time.Duration `mapstructure:"max_wait"`
	// Cooldown moves a target to the end of the candidate order after a
	// connect failure. 0 disables the cooldown.
	Cooldown time.Duration `mapstructure:"cooldown"`
	// AttemptTimeout bounds one gRPC stream-creation attempt. HTTP uses
	// upstream_http_dial_timeout and upstream_http_tls_handshake_timeout.
	AttemptTimeout time.Duration `mapstructure:"attempt_timeout"`
}

// Policy converts the configuration into a failover engine policy.
func (f FailoverConfig) Policy() failover.Policy {
	return failover.Policy{
		Enabled:           f.Enabled,
		MaxTargets:        f.MaxTargets,
		Retries:           f.Retries,
		BackoffInitial:    f.BackoffInitial,
		BackoffMultiplier: f.BackoffMultiplier,
		BackoffMax:        f.BackoffMax,
		BackoffJitter:     f.BackoffJitter,
		MaxWait:           f.MaxWait,
		Cooldown:          f.Cooldown,
		AttemptTimeout:    f.AttemptTimeout,
	}
}

func setFailoverDefaults(v *viper.Viper) {
	for _, key := range []string{failoverPrimaryKey, failoverShadowKey} {
		v.SetDefault(key+".enabled", false)
		v.SetDefault(key+".max_targets", 0)
		v.SetDefault(key+".retries", 0)
		v.SetDefault(key+".backoff_initial", defaultFailoverBackoffInitial)
		v.SetDefault(key+".backoff_multiplier", defaultFailoverBackoffMultiplier)
		v.SetDefault(key+".backoff_max", defaultFailoverBackoffMax)
		v.SetDefault(key+".backoff_jitter", defaultFailoverBackoffJitter)
		v.SetDefault(key+".max_wait", defaultFailoverMaxWait)
		v.SetDefault(key+".cooldown", defaultFailoverCooldown)
		v.SetDefault(key+".attempt_timeout", defaultFailoverAttemptTimeout)
	}
}

func normalizeFailoverConfig(cfg *Config) error {
	if err := validateFailoverConfig(cfg.Protocol, failoverPrimaryKey, cfg.PrimaryFailover); err != nil {
		return err
	}

	return validateFailoverConfig(cfg.Protocol, failoverShadowKey, cfg.ShadowFailover)
}

func validateFailoverConfig(protocol, field string, failover FailoverConfig) error {
	if failover.Enabled && protocol == protocolMilter {
		return fmt.Errorf("%s.enabled is not supported with protocol milter", field)
	}

	checks := []struct {
		ok      bool
		message string
	}{
		{failover.MaxTargets >= 0, "max_targets must be >= 0"},
		{failover.Retries >= 0 && failover.Retries <= maxFailoverRetries, fmt.Sprintf("retries must be between 0 and %d", maxFailoverRetries)},
		{failover.BackoffInitial > 0, "backoff_initial must be > 0"},
		{failover.BackoffMax >= failover.BackoffInitial, "backoff_max must be >= backoff_initial"},
		{failover.BackoffMultiplier >= 1, "backoff_multiplier must be >= 1"},
		{failover.BackoffJitter >= 0 && failover.BackoffJitter <= 1, "backoff_jitter must be between 0 and 1"},
		{failover.MaxWait >= 0, "max_wait must be >= 0"},
		{failover.Cooldown >= 0, "cooldown must be >= 0"},
		{failover.AttemptTimeout > 0, "attempt_timeout must be > 0"},
		{
			failover.BackoffMax <= maxFailoverDuration && failover.MaxWait <= maxFailoverDuration &&
				failover.Cooldown <= maxFailoverDuration && failover.AttemptTimeout <= maxFailoverDuration,
			"durations must not exceed " + maxFailoverDuration.String(),
		},
	}

	for _, check := range checks {
		if !check.ok {
			return fmt.Errorf("%s.%s", field, check.message)
		}
	}

	return nil
}
