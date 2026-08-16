# 0. 创建目录并进入
$TlsDir = ".\etc\wallet\tls"
if (!(Test-Path -Path $TlsDir)) {
    New-Item -ItemType Directory -Path $TlsDir -Force | Out-Null
}
Set-Location -Path $TlsDir

# 1. 生成 CA 私钥与自签 CA 证书  
openssl genrsa -out ca.key 4096  
openssl req -x509 -new -nodes -key ca.key -sha256 -days 3650 -subj "/CN=wallet-internal-ca" -out ca.crt  

# 2. 生成服务端私钥 + CSR（CN 用 sign.internal）  
openssl genrsa -out sign.key 4096  
openssl req -new -key sign.key -subj "/CN=sign.internal" -out sign.csr  

# 3. 生成 SAN 临时配置文件并用 CA 签发服务端证书
# (解决 Windows 环境下 bash <(printf ...) 无法直接调用的问题)
"subjectAltName=DNS:sign.internal" | Out-File -FilePath san.ext -Encoding ascii
openssl x509 -req -in sign.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -sha256 -out sign.crt -extfile san.ext
Remove-Item -Path san.ext -ErrorAction SilentlyContinue

# 4. 生成客户端（worker）证书，供调用方使用  
openssl genrsa -out client.key 4096  
openssl req -new -key client.key -subj "/CN=wallet-worker" -out client.csr  
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -sha256 -out client.crt

Write-Host "TLS 证书已成功生成至目录: $TlsDir" -ForegroundColor Green