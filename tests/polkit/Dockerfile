# Integration test: real D-Bus and polkit, no host mounts or privileged container.
FROM debian:12-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends polkitd dbus systemd procps \
 && rm -rf /var/lib/apt/lists/*
RUN useradd --system --user-group vibeguard
COPY --chmod=0644 systemd/50-vibeguard.rules /etc/polkit-1/rules.d/50-vibeguard.rules
COPY systemd/check-polkit /check-polkit
COPY tests/polkit/check.sh /check.sh
CMD ["sh", "/check.sh"]
