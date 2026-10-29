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
      systemd systemd-sysv ifupdown isc-dhcp-client iproute2 iptables procps \
      wireguard-tools openresolv ca-certificates polkitd \
 && apt-get clean && rm -rf /var/lib/apt/lists/* \
 && rm -f /etc/apt/apt.conf.d/docker-* /etc/dpkg/dpkg.cfg.d/docker*
COPY --from=build /vibeguard /usr/local/bin/vibeguard
COPY lxc/vibeguard.service lxc/vibeguard-firstboot.service /etc/systemd/system/
COPY lxc/vibeguard-firstboot /usr/local/sbin/vibeguard-firstboot
COPY systemd/vibeguard-wg-quick /usr/local/libexec/vibeguard-wg-quick
COPY systemd/vibeguard-network /usr/local/libexec/vibeguard-network
COPY --chmod=0644 systemd/vibeguard-network.env /etc/vibeguard-network.env
COPY systemd/wg-quick-vibeguard.conf /etc/systemd/system/wg-quick@.service.d/vibeguard.conf
COPY --chmod=0644 systemd/50-vibeguard.rules /etc/polkit-1/rules.d/50-vibeguard.rules
RUN chmod 755 /usr/local/libexec/vibeguard-wg-quick /usr/local/libexec/vibeguard-network
RUN useradd --system --user-group --home-dir /var/lib/vibeguard --no-create-home \
      --shell /usr/sbin/nologin vibeguard \
 && printf 'net.ipv4.ip_forward=1\nnet.ipv6.conf.all.forwarding=1\n' > /etc/sysctl.d/99-vibeguard.conf \
 && systemctl enable vibeguard.service vibeguard-firstboot.service \
 && : > /etc/machine-id && rm -f /var/lib/dbus/machine-id
