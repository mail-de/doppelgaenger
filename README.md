# HTTP Shadow Proxy

A high-performance HTTP proxy written in Go that mirrors a percentage of incoming traffic to a shadow backend for testing and comparison purposes, while serving requests via a primary backend.

## Features

- **Traffic Shadowing**: Mirrored requests to a shadow backend without affecting the primary response.
- **Sampling & Rate Limiting**: Configure the percentage of traffic to shadow and apply rate limits (RPS/Burst) to protect the shadow environment.
- **Response Comparison**: Automatically compares headers between primary and shadow responses and logs differences.
- **Structured Logging**: Uses `slog` for detailed, machine-readable logs including performance metrics and header diffs.
- **TLS Support**: Supports both incoming TLS and secure communication with upstream backends (with custom CA support).
- **Concurrency Control**: Managed worker pools and queues for both primary and shadow backends.

## Configuration

The application is configured via environment variables:

### General
| Variable | Description | Default |
|----------|-------------|---------|
| `LISTEN` | Listen address for the proxy | `:8443` |
| `TLS_CERT` | Path to TLS certificate file | (optional) |
| `TLS_KEY` | Path to TLS key file | (optional) |

### Upstream Backends
| Variable | Description | Default |
|----------|-------------|---------|
| `PRIMARY` | URL of the primary backend | `https://127.0.0.1:9001` |
| `SHADOW` | URL of the shadow backend | `https://127.0.0.1:9002` |
| `INSECURE_UPSTREAM` | Skip TLS verification for upstreams | `false` |
| `ROOT_CA` | Path to a custom CA for all upstreams | (optional) |
| `PRIMARY_ROOT_CA` | Path to a custom CA for primary upstream | (optional) |
| `SHADOW_ROOT_CA` | Path to a custom CA for shadow upstream | (optional) |

### Shadowing & Rate Limiting
| Variable | Description | Default |
|----------|-------------|---------|
| `SHADOW_SAMPLE_PERCENT` | Percentage of requests to shadow (0-100) | `5` |
| `SHADOW_FORCE_HEADER` | Header to force shadowing for a request | `X-Shadow` |
| `SHADOW_RPS` | Maximum requests per second to shadow | `200.0` |
| `SHADOW_BURST` | Token bucket burst size for shadowing | `400` |
| `SHADOW_TIMEOUT` | Timeout for shadow requests | `150ms` |

### Worker Pools
| Variable | Description | Default |
|----------|-------------|---------|
| `PRIMARY_WORKERS` | Number of workers for primary requests | `32` |
| `SHADOW_WORKERS` | Number of workers for shadow requests | `16` |
| `PRIMARY_QUEUE` | Queue length for primary requests | `4096` |
| `SHADOW_QUEUE` | Queue length for shadow requests | `4096` |

### Logging & Headers
| Variable | Description | Default |
|----------|-------------|---------|
| `LOG_SESSION_ONLY_ON_DIFF` | Only log session headers if there's a diff | `true` |

## How It Works

1. **Request Arrival**: The proxy receives an HTTP(S) request.
2. **Primary Request**: The request is always forwarded to the `PRIMARY` backend. The response from this backend is returned to the client.
3. **Shadow Decision**: Based on `SHADOW_SAMPLE_PERCENT` or the presence of `SHADOW_FORCE_HEADER`, the proxy decides whether to shadow the request.
4. **Shadow Request**: If selected, the request is sent to the `SHADOW` backend asynchronously.
5. **Comparison**: The proxy compares specific headers (e.g., `Auth-Status`, `Auth-User`) between the primary and shadow responses.
6. **Logging**: A single structured log line is generated containing details about both requests, including durations and any header differences found.

## Building and Running

### Prerequisites
- Go 1.25 or later

### Build
```bash
go build -o httpproxy main.go
```

### SBOM
```bash
make sbom
```
Generates `sbom.cdx.json` in the project directory. During the Docker image build, the SBOM is copied into the image as `/app/sbom.cdx.json`.

### Run
```bash
export PRIMARY="https://api.production.internal"
export SHADOW="https://api.staging.internal"
./httpproxy
```

## License

[Add License Information Here, e.g., MIT]
