FROM debian:13-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends iproute2 iptables procps python3 \
 && rm -rf /var/lib/apt/lists/*
COPY --chmod=0755 systemd/vibeguard-network /usr/local/libexec/vibeguard-network
COPY tests/network/check.sh /check.sh
CMD ["bash", "/check.sh"]
