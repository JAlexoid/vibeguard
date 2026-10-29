FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /vibeguard .

FROM alpine:3.20
RUN apk add --no-cache wireguard-tools iptables ip6tables bash openresolv iproute2
COPY --from=build /vibeguard /usr/local/bin/vibeguard
COPY --chmod=0755 systemd/vibeguard-network /usr/local/libexec/vibeguard-network
ENV VIBEGUARD_DATA=/var/lib/vibeguard/data.json
VOLUME ["/var/lib/vibeguard", "/etc/wireguard"]
EXPOSE 8080
ENTRYPOINT ["vibeguard"]
