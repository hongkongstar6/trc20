#!/usr/bin/env bash
# 生成 sign-service mTLS 所需的自签证书：CA、服务端证书、客户端证书。
#
#   bash deploy/tls/gen-certs.sh            # 输出到 etc/tls
#   OUT_DIR=/etc/wallet/tls bash deploy/tls/gen-certs.sh
#
# 服务端证书的 SAN 必须同时包含 config 里的 sign.tls.server_name（sign.internal）
# 与实际连接用的主机名（docker compose 里是 sign），否则握手时校验会失败。
set -euo pipefail

OUT_DIR=${OUT_DIR:-etc/tls}
DAYS=${DAYS:-3650}
SERVER_NAME=${SERVER_NAME:-sign.internal}
SERVER_ALT=${SERVER_ALT:-sign}

mkdir -p "$OUT_DIR"
cd "$OUT_DIR"

# CA
openssl genrsa -out ca.key 4096
openssl req -x509 -new -nodes -key ca.key -sha256 -days "$DAYS" \
    -subj "/CN=wallet-internal-ca" -out ca.crt

# 服务端
openssl genrsa -out sign.key 2048
openssl req -new -key sign.key -subj "/CN=${SERVER_NAME}" -out sign.csr
cat >sign.ext <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=DNS:${SERVER_NAME},DNS:${SERVER_ALT},DNS:localhost,IP:127.0.0.1
EOF
openssl x509 -req -in sign.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
    -days "$DAYS" -sha256 -extfile sign.ext -out sign.crt

# 客户端（api / withdraw / sweep）
openssl genrsa -out client.key 2048
openssl req -new -key client.key -subj "/CN=wallet-worker" -out client.csr
cat >client.ext <<EOF
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=clientAuth
EOF
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
    -days "$DAYS" -sha256 -extfile client.ext -out client.crt

rm -f sign.ext client.ext
chmod 644 ./*.crt
chmod 600 ./*.key
echo "证书已生成于 $(pwd)"
