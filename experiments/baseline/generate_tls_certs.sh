#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CERT_DIR="${1:-$ROOT_DIR/experiments/baseline/certs}"
DAYS="${DAYS:-3650}"

mkdir -p "$CERT_DIR"

CA_KEY="$CERT_DIR/ca.key.pem"
CA_CERT="$CERT_DIR/ca.cert.pem"
SERVER_KEY="$CERT_DIR/server.key.pem"
SERVER_CSR="$CERT_DIR/server.csr.pem"
SERVER_CERT="$CERT_DIR/server.cert.pem"
SERVER_EXT="$CERT_DIR/server.ext.cnf"

openssl genrsa -out "$CA_KEY" 4096
openssl req -x509 -new -nodes -key "$CA_KEY" -sha256 -days "$DAYS" \
  -subj "/C=US/ST=Lab/L=Lab/O=MQTT-NG/OU=Baseline/CN=MQTT-NG Test CA" \
  -out "$CA_CERT"

openssl genrsa -out "$SERVER_KEY" 2048
openssl req -new -key "$SERVER_KEY" \
  -subj "/C=US/ST=Lab/L=Lab/O=MQTT-NG/OU=Baseline/CN=localhost" \
  -out "$SERVER_CSR"

cat >"$SERVER_EXT" <<'EOF'
authorityKeyIdentifier=keyid,issuer
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=@alt_names

[alt_names]
DNS.1=localhost
IP.1=127.0.0.1
EOF

openssl x509 -req -in "$SERVER_CSR" -CA "$CA_CERT" -CAkey "$CA_KEY" -CAcreateserial \
  -out "$SERVER_CERT" -days "$DAYS" -sha256 -extfile "$SERVER_EXT"

chmod 600 "$CA_KEY" "$SERVER_KEY"

echo "Generated TLS certificates in: $CERT_DIR"
echo "CA cert: $CA_CERT"
echo "Server cert: $SERVER_CERT"
echo "Server key: $SERVER_KEY"
