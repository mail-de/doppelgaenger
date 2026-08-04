# Deployment

This repository supports a direct binary, a scratch-based container image, and
systemd socket activation. The shipped Docker Compose file is a local HTTP demo,
not a production topology.

## Build the binary

The project requires Go 1.26.3 or later and uses the vendored module tree.

```sh
make build
./build/doppelgaenger --version
./build/doppelgaenger --config /etc/doppelgaenger/config.yaml
```

`make build` injects `git describe --tags --always --dirty` as the version. The
process writes logs to stdout and stderr; it does not manage log files.

The supported CLI options are `--config`/`-c`, `--version`, and
`--help`/`-h`. Positional arguments are rejected.

## Container image

```sh
make docker-build
docker run --rm \
  -p 8443:8443 \
  -v "$PWD/config.docker.yaml:/app/config.yaml:ro" \
  -v "$PWD/certs:/certs:ro" \
  doppelgaenger
```

The final image is `scratch`. It contains the proxy, the MIT license at
`/app/LICENSE`, the generated CycloneDX SBOM at `/app/sbom.cdx.json`, CA
certificates, and UTC zone data. It does not contain a shell or an application
configuration. Mount the configuration and every referenced certificate or CA
explicitly.

The image declares port 8443 because it is paired with `config.docker.yaml`.
Other protocol ports must be published explicitly.

## Local Docker Compose demo

```sh
# Create local-only TLS files first; see certs/README.md.
docker compose up --build
```

Compose starts the HTTP proxy plus Primary and Shadow fake HTTP servers. The
proxy is published on 8443, runs as UID/GID 1000, drops all Linux capabilities,
uses a 256 MiB memory limit, and mounts configuration and certificates read
only. The fake backends are development tools and must not be treated as
production services.

The Compose file does not include gRPC or Milter backends and does not enable
the observability port.

## systemd socket activation

The repository ships `doppelgaenger.service` and `doppelgaenger.socket` as an
HTTP example. Install them only after reviewing paths, port, protocol, user,
and hardening for the target host.

The supplied socket listens on port 8080 with descriptor name `http`. The
service reads `/etc/doppelgaenger/config.yaml` and starts
`/usr/local/bin/doppelgaenger`. Keep `listen_addr` aligned with the socket even
though descriptor-name matching takes priority.

```sh
sudo install -m 0755 build/doppelgaenger /usr/local/bin/doppelgaenger
sudo install -m 0644 doppelgaenger.service /etc/systemd/system/
sudo install -m 0644 doppelgaenger.socket /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now doppelgaenger.socket doppelgaenger.service
```

Activated listeners are selected first by descriptor name (`http`, `grpc`, or
`milter`), then by expected TCP port, then by the first passed listener. Extra
listeners are closed. For gRPC or Milter, provide a reviewed protocol-specific
socket unit with the matching descriptor name and address.

Inbound HTTP TLS can wrap an activated TCP socket when both HTTP certificate
paths are configured.

## Reload and shutdown

The standalone Unix process handles SIGHUP by validating configuration and then
re-executing itself. This preserves the PID but not active connections.

The bundled systemd unit uses a different operational path:

```sh
sudo systemctl reload doppelgaenger.service
```

Its `ExecReload` asks systemd to asynchronously `try-restart` the service. Use
`systemctl status` and readiness after the command; the command returning does
not prove the replacement process is ready.

On normal shutdown, HTTP gets a five-second graceful shutdown context, gRPC
attempts graceful stop until its lifecycle context expires, and Milter closes
the listener. Existing Milter connection goroutines are not centrally drained
by the server object.

## Certificates and secrets

The local certificate recipe is in [`certs/README.md`](../../certs/README.md).
Those files are for localhost development and are ignored by Git. Production
deployments should mount certificate and secret material from their normal
secret-management path.

Prefer `grpc_backend_oidc_auth.client_secret_env` over an inline client secret.
Do not place Prometheus Basic Auth credentials, OTLP authorization headers, or
private keys in a committed configuration.

## SBOM

```sh
make sbom
```

This writes `sbom.cdx.json` in the repository. Container builds generate and

The MIT license covers project-owned work. Dependencies listed by the SBOM and
vendored in the repository retain their own licenses.
