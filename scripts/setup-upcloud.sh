#!/usr/bin/env bash
set -euo pipefail

HOST="${UPCLOUD_HOST:?Set UPCLOUD_HOST to your server IP}"
USER="${UPCLOUD_USER:-root}"

REPO_ROOT="$(git rev-parse --show-toplevel)"

echo "Bootstrapping $USER@$HOST..."

ssh "$USER@$HOST" bash << 'REMOTE'
set -euo pipefail

echo "--- swap ---"
if [ ! -f /swapfile ]; then
  fallocate -l 2G /swapfile
  chmod 600 /swapfile
  mkswap /swapfile
  swapon /swapfile
  echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi
sysctl -w vm.swappiness=10
grep -q 'vm.swappiness' /etc/sysctl.conf || echo 'vm.swappiness=10' >> /etc/sysctl.conf

echo "--- docker ---"
if ! command -v docker &>/dev/null; then
  apt-get update -y -q
  apt-get install -y -q ca-certificates curl
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
    https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -y -q
  apt-get install -y -q docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  systemctl enable --now docker
else
  echo "docker already installed, skipping"
fi

echo "--- app directory ---"
mkdir -p /opt/mate-relay/data /opt/mate-relay/certs
chmod 700 /opt/mate-relay/data /opt/mate-relay/certs

echo "--- firewall ---"
apt-get install -y -q ufw
ufw allow 22
ufw allow 80
ufw allow 443
ufw --force enable

echo "--- env file ---"
if [ ! -f /opt/mate-relay/.env ]; then
  cat > /opt/mate-relay/.env << 'EOF'
RELAY_IMAGE=ghcr.io/mqtik/mate-relay:latest
LETSENCRYPT_EMAIL=
ADMIN_BEARER_SECRET=
DEVICE_TOKEN_SECRET=
CODE_HASH_PEPPER=
PUBLIC_BASE_URL=https://tunnel.mate.iwwwan.com
CONTROL_HOST=tunnel.mate.iwwwan.com
EOF
  echo "Created /opt/mate-relay/.env from template — fill in the values before starting"
else
  echo "/opt/mate-relay/.env already exists, leaving it untouched"
fi

echo "Server-side bootstrap done"
REMOTE

echo "--- copying compose files ---"
scp "$REPO_ROOT/deploy/compose.prod.yaml" "$USER@$HOST:/opt/mate-relay/compose.prod.yaml"

echo ""
echo "Bootstrap complete. Remaining manual steps:"
echo ""
echo "1. Fill in /opt/mate-relay/.env on the server:"
echo "   ssh $USER@$HOST 'nano /opt/mate-relay/.env'"
echo ""
echo "2. Add these secrets in GitHub → Settings → Secrets → Actions:"
echo "   UPCLOUD_HOST          = $HOST"
echo "   UPCLOUD_USER          = $USER"
echo "   UPCLOUD_SSH_KEY       = (contents of your private deploy key)"
echo "   UPCLOUD_SSH_PASSPHRASE= (passphrase for that key, or leave empty)"
echo ""
echo "3. Push to main — CI will build, push to GHCR, and deploy automatically."
