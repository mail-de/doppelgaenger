# Documentation

This index is the recommended entry point after the root README. Documents are
grouped by the task a reader is trying to complete.

## Understand the system

- [How Doppelgaenger works](concepts.md): Primary and Shadow roles, request
  flow, guarantees, and deliberate limits
- [FAQ](faq.md): concise answers to common operational and design questions
- [MIT License](../LICENSE): terms for project-owned source and documentation

## Operator guides

- [Configuration](operator/configuration.md): file lookup, shared settings,
  defaults, validation, logging, and runtime isolation
- [HTTP mode](operator/http.md): backends, routing rules, comparison, TLS, and
  failure behavior
- [gRPC mode](operator/grpc.md): generic proxying, targets, TLS/mTLS, rules,
  comparison modes, streaming, and OIDC client credentials
- [Milter mode](operator/milter.md): session behavior, the fixed no-reply
  profile, asynchronous Shadow processing, and MTA integration boundaries
- [Deployment](operator/deployment.md): binary, Docker Compose, systemd socket
  activation, reload, shutdown, and local certificates
- [Observability](operator/observability.md): readiness, logs, Prometheus,
  OpenTelemetry, metric labels, and trace propagation
- [Operations](operator/operations.md): preflight, rollout, verification,
  troubleshooting, and rollback

## Tutorials

- [Shadow an HTTP endpoint](tutorials/http-first-shadow.md)
- [Shadow a generic gRPC method](tutorials/grpc-first-shadow.md)
- [Roll out Doppelgaenger in front of a Milter](tutorials/milter-rollout.md)

Tutorials are intentionally small. For production decisions, follow the linked
operator guides and the annotated [`config.yaml`](../config.yaml).

## Developer guides

- [Architecture](developer/architecture.md): component boundaries and
  protocol-specific data paths
- [Development](developer/development.md): toolchain, workflow, code changes,
  and Definition of Done
- [Testing](developer/testing.md): unit, race, E2E, Docker, and coverage intent
- [Releases and automation](developer/releases.md): branches, GitHub Actions,
  release tags, artifacts, images, SBOMs, and provenance
- [Test tools](developer/test-tools.md): the fake backends, probes, and fake
  OTLP collector shipped in this repository

## Source-of-truth order

Documentation explains the supported behavior, but executable artifacts remain
authoritative when a conflict is found:

1. Configuration validation in `internal/config`
2. Protocol implementation under `internal/`
3. Focused unit and E2E tests
4. The annotated `config.yaml` and these documents

If code changes alter operator-visible behavior, update the affected document
and example configuration in the same change.
