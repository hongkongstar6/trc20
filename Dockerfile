# Multi-stage build producing one image with all five entrypoints. Which one
# runs is decided by the container command, so every service shares the same
# audited artifact.
# Multi-stage build producing one image with all five entrypoints.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 wallet

WORKDIR /app

# 显式将文件所有权赋予 wallet 用户
COPY --chown=wallet:wallet bin/ /app/
COPY --chown=wallet:wallet configs/ /app/configs/

# 确保二进制文件具备可执行权限，并准备日志目录（log_dir 默认 ./log -> /app/log）
RUN chmod +x /app/* \
    && mkdir -p /app/log \
    && chown wallet:wallet /app/log

USER wallet

ENTRYPOINT ["/app/api"]
