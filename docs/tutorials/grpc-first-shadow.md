# Tutorial: shadow a generic gRPC method

This tutorial starts two fake gRPC backends and sends an opaque unary request
through Doppelgaenger. It needs four terminal sessions.

## 1. Build the binaries

```sh
make build
go build -mod=vendor -o build/fakegrpcserver ./cmd/fakegrpcserver
go build -mod=vendor -o build/grpcprobe ./cmd/grpcprobe
```

## 2. Create a tutorial configuration

Save the following as `/tmp/doppelgaenger-grpc-tutorial.yaml`:

```yaml
protocol: grpc
grpc_listen_addr: "127.0.0.1:19444"
primary_grpc_targets:
  - name: primary
    address: "127.0.0.1:19445"
    tls:
      enabled: false
shadow_grpc_targets:
  - name: shadow
    address: "127.0.0.1:19446"
    tls:
      enabled: false
shadow_sample_percent: 0
shadow_rps: 0
grpc_shadow_force_metadata: "x-shadow"
grpc_shadow_timeout: "500ms"
grpc_compare_mode: message_hash
log_json: true
```

Sampling is off, so only the explicit metadata probe starts Shadow work.

## 3. Start both backends

In terminal 1:

```sh
./build/fakegrpcserver \
  -listen 127.0.0.1:19445 \
  -mode unary-echo \
  -response-prefix 'primary:'
```

In terminal 2:

```sh
./build/fakegrpcserver \
  -listen 127.0.0.1:19446 \
  -mode unary-echo \
  -response-prefix 'shadow:'
```

## 4. Start Doppelgaenger

In terminal 3:

```sh
./build/doppelgaenger \
  --config /tmp/doppelgaenger-grpc-tutorial.yaml
```

## 5. Send a forced Shadow RPC

In terminal 4:

```sh
./build/grpcprobe \
  -addr 127.0.0.1:19444 \
  -method /tutorial.Echo/Unary \
  -mode unary \
  -payload hello \
  -metadata x-shadow=1 \
  -expect-status OK \
  -expect-count 1 \
  -expect-message 'primary:hello'
```

The probe must receive `primary:hello`. The different Shadow prefix should
produce a `grpc_proxy` event with `compare_outcome=diff` and a message-hash
difference. This demonstrates both properties at once: Shadow ran, and its
different response did not replace Primary.

Stop the three long-running processes with Ctrl-C. Continue with
[gRPC mode](../operator/grpc.md) before using TLS, rules, streaming, or OIDC.
