FROM debian:13
RUN apt-get update && apt-get install -y --no-install-recommends avahi-daemon iproute2 \
 && rm -rf /var/lib/apt/lists/*
COPY vibeguard /usr/local/bin/vibeguard
COPY tests/avahi/check.sh /check.sh
CMD ["sh", "/check.sh"]
