# Root filesystem for the VibeGuard LXC template (built by `make lxc-template`).
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /vibeguard .

FROM debian:12
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      systemd systemd-sysv ifupdown isc-dhcp-client iproute2 iptables \
      wireguard-tools openresolv ca-certificates \
 && apt-get clean && rm -rf /var/lib/apt/lists/* \
 && rm -f /etc/apt/apt.conf.d/docker-* /etc/dpkg/dpkg.cfg.d/docker*
COPY --from=build /vibeguard /usr/local/bin/vibeguard
COPY lxc/vibeguard.service lxc/vibeguard-firstboot.service /etc/systemd/system/
COPY lxc/vibeguard-firstboot /usr/local/sbin/vibeguard-firstboot
RUN mkdir -p /var/lib/vibeguard /etc/wireguard \
 && chmod 700 /var/lib/vibeguard /etc/wireguard \
 && echo 'net.ipv4.ip_forward=1' > /etc/sysctl.d/99-vibeguard.conf \
 && systemctl enable vibeguard.service vibeguard-firstboot.service \
 && : > /etc/machine-id && rm -f /var/lib/dbus/machine-id
