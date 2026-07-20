# Local development certificates

The Docker Compose setup mounts this directory read-only and expects
`cert.pem` plus `key.pem`. These files are local TLS material and must never be
committed.

Create a localhost-only development pair with:

```sh
openssl req -x509 -newkey rsa:3072 -sha256 -nodes \
  -keyout certs/key.pem -out certs/cert.pem -days 30 \
  -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
chmod 600 certs/key.pem
```

Production deployments must mount certificate material from their normal
secret-management path instead.
