# Tutorial: shadow an HTTP endpoint

This tutorial uses the repository's Docker Compose demo. It proves the local
Primary path first, then forces one Shadow request. It is not a production
deployment recipe.

## 1. Create local TLS material

Follow [`certs/README.md`](../../certs/README.md) to create `certs/cert.pem` and
`certs/key.pem`. The certificate is self-signed and intended only for localhost.

## 2. Start the demo

```sh
docker compose up --build --detach
docker compose ps
```

Compose starts Doppelgaenger on HTTPS port 8443 plus one Primary and one Shadow
fake HTTP backend.

## 3. Prove the Primary path

```sh
curl --insecure --include \
  https://127.0.0.1:8443/tutorial
```

`--insecure` is appropriate only for this generated localhost certificate. The
response came from Primary regardless of whether the configured 5% sampler
happened to start Shadow work.

## 4. Force one Shadow request

```sh
curl --insecure --include \
  -H 'X-Shadow: tutorial' \
  -H 'X-Request-ID: http-tutorial-1' \
  https://127.0.0.1:8443/tutorial
```

The force header is allowed because the demo has no `path_rules` entry with
`shadow: never`.

## 5. Inspect the result

```sh
docker compose logs --no-log-prefix doppelgaenger
```

Find the `auth_proxy` event for `x_request_id=http-tutorial-1`. Check:

- `primary_selected` names the Primary fake backend.
- `shadow_forced=true` and `shadow_started=true`.
- `shadow_selected` names the Shadow fake backend.
- `shadow_err` and `compare_err` are empty.
- `diff` reflects only the configured comparison fields.

The HTTP response itself remains the Primary response.

## 6. Stop the demo

```sh
docker compose down
```

For production design, continue with [HTTP mode](../operator/http.md),
[Deployment](../operator/deployment.md), and
[Operations](../operator/operations.md).
