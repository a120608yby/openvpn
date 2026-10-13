FROM golang:1.25.4-alpine AS web

# 默认使用腾讯云 Go 模块代理。发布工作流会改回官方代理。
ARG GOPROXY=https://mirrors.cloud.tencent.com/go,direct
ARG TARGETARCH
ARG TARGETVARIANT

ENV GOPROXY=${GOPROXY}
WORKDIR /src

COPY src/openvpn-web/go.mod src/openvpn-web/go.sum ./
RUN go mod download

COPY src/openvpn-web/ ./
RUN set -eux; \
    arch="${TARGETARCH:-$(go env GOARCH)}"; \
    goarm=""; \
    if [ "$arch" = "arm" ]; then \
      case "${TARGETVARIANT}" in \
        v6) goarm=6 ;; \
        *) goarm=7 ;; \
      esac; \
    fi; \
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" GOARM="$goarm" \
      go build -trimpath -ldflags="-s -w" -o /out/openvpn-web .

FROM alpine:3.23.4

# 默认使用腾讯云 Alpine 镜像加速国内构建。发布工作流会改回官方源。
ARG ALPINE_MIRROR=mirrors.cloud.tencent.com
RUN sed -i "s/dl-cdn.alpinelinux.org/${ALPINE_MIRROR}/g" /etc/apk/repositories \
    && apk add --no-cache openssl openvpn iptables iptables-legacy bash supervisor curl sqlite grep mailcap coreutils jq nftables
#RUN mkdir /lib64 && ln -s /lib/libc.musl-x86_64.so.1 /lib64/ld-linux-x86-64.so.2

COPY build/docker-entrypoint.sh /usr/bin/docker-entrypoint.sh
COPY build/openvpn-auth /usr/lib/openvpn/plugins/openvpn-auth
COPY build/supervisord.conf /etc/supervisor/conf.d/supervisord.conf
COPY --from=web /out/openvpn-web /usr/local/bin/openvpn-web
# Windows 检出可能是 CRLF，内核会把 shebang 当成不存在的解释器。
RUN sed -i 's/\r$//' /usr/bin/docker-entrypoint.sh /usr/lib/openvpn/plugins/openvpn-auth /etc/supervisor/conf.d/supervisord.conf

RUN wget https://github.com/OpenVPN/easy-rsa/releases/download/v3.2.6/EasyRSA-3.2.6.tgz && tar -ozxf EasyRSA-3.2.6.tgz && mv EasyRSA-3.2.6 /usr/share/easy-rsa && rm -rf EasyRSA-3.2.6.tgz
RUN chmod +x /usr/local/bin/openvpn-web /usr/bin/docker-entrypoint.sh /usr/lib/openvpn/plugins/openvpn-auth
RUN ln -s /usr/share/easy-rsa/easyrsa /usr/local/bin

VOLUME ["/data"]

ENV GIN_MODE=release
ENV OVPN_DATA=/data

EXPOSE 1194/udp 8833

USER root

WORKDIR /data


ENTRYPOINT  ["docker-entrypoint.sh"]

CMD ["/usr/bin/supervisord"]
