# mate-relay

A TLS tunnel relay for the Mate app. Devices connect to `tunnel.mate.iwwwan.com`; the relay uses SNI routing to proxy connections between iOS/Android clients and their paired Mac tunnel agents. Let's Encrypt certificates are obtained automatically via the ACME TLS-ALPN challenge.

## How it works

1. A Mac registers with the relay over a persistent WebSocket at `GET /tunnel/control` (authenticated via a device token).
2. An iOS/Android client opens a TLS connection to `<macID>.tunnel.mate.iwwwan.com:443`. The relay reads the SNI hostname, routes the connection to the waiting Mac control socket, and splices the streams.
3. Admin endpoints (`/admin/codes`, `/admin/devices`) are protected by a bearer token and let you create one-time enrollment codes and revoke devices.

## Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `ADMIN_BEARER_SECRET` | yes | — | Bearer token for `/admin/*` endpoints |
| `DEVICE_TOKEN_SECRET` | yes | — | HMAC secret for device JWT signing |
| `CODE_HASH_PEPPER` | yes | — | Pepper for hashing one-time enrollment codes |
| `LETSENCRYPT_EMAIL` | yes (prod) | — | Email for Let's Encrypt account registration |
| `CONTROL_HOST` | no | `tunnel.mate.iwwwan.com` | The apex hostname; device subdomains are `<id>.<CONTROL_HOST>` |
| `PUBLIC_BASE_URL` | no | `https://tunnel.mate.iwwwan.com` | Base URL returned in API responses |
| `DATA_DIR` | no | `/data` | Directory for SQLite database |
| `CERT_DIR` | no | `/certs` | Directory for Let's Encrypt certificate cache |
| `LISTEN_ADDR` | no | `:443` | TLS listener address |
| `HTTP_ADDR` | no | `:80` | Plaintext listener for ACME HTTP-01 challenge redirect |
| `LOG_LEVEL` | no | `info` | Log verbosity |
| `DEV` | no | `false` | Dev mode: uses a self-signed cert; `LETSENCRYPT_EMAIL` not required |

## Running locally

```bash
mkdir -p testdata/certs

DEV=true \
  DATA_DIR=./testdata \
  CERT_DIR=./testdata/certs \
  ADMIN_BEARER_SECRET=secret \
  DEVICE_TOKEN_SECRET=secret \
  CODE_HASH_PEPPER=pepper \
  CONTROL_HOST=localhost \
  ./mate-relay
```

Or build and run in one step:

```bash
go build -o mate-relay ./cmd/mate-relay && DEV=true DATA_DIR=./testdata CERT_DIR=./testdata/certs ADMIN_BEARER_SECRET=s DEVICE_TOKEN_SECRET=s CODE_HASH_PEPPER=p CONTROL_HOST=localhost ./mate-relay
```

## Enrolling a device

1. Create an enrollment code (valid for 24 h by default):

```bash
curl -s -X POST https://tunnel.mate.iwwwan.com/admin/codes \
  -H "Authorization: Bearer $ADMIN_BEARER_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"label":"my-mac"}' | jq .
# { "id": "...", "code": "XXXX-XXXX-XXXX" }
```

2. The Mate app on the Mac redeems the code at `POST /redeem` and receives a device token. It then connects to `GET /tunnel/control` with that token.

## Deploying

See `deploy/` for the Docker Compose production setup and `scripts/setup-upcloud.sh` to bootstrap the UpCloud server. CI (`.github/workflows/deploy.yml`) builds and pushes the image to GHCR on every push to `main`, then deploys to the server via SSH.

Quick start:

```bash
UPCLOUD_HOST=209.151.155.156 UPCLOUD_USER=root bash scripts/setup-upcloud.sh
ssh root@209.151.155.156 'nano /opt/mate-relay/.env'
git push origin main
```
