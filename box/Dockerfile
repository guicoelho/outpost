FROM ubuntu:22.04

ARG TTYD_VERSION=1.7.7
ARG OPENCODE_VERSION=0.0.55
ARG CADDY_VERSION=2.11.0

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        curl \
        wget \
        git \
        jq \
        unzip \
        inotify-tools \
        ca-certificates \
        gnupg \
        software-properties-common \
        postgresql-client \
    && add-apt-repository -y ppa:deadsnakes/ppa \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        python3.11 \
        python3.11-venv \
        python3-pip \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && npm install -g pm2 \
    && wget -q -O /usr/local/bin/ttyd https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.x86_64 \
    && chmod +x /usr/local/bin/ttyd \
    && curl -fsSL https://opencode.ai/install | VERSION=${OPENCODE_VERSION} bash \
    && if [ -x /root/.local/bin/opencode ]; then ln -sf /root/.local/bin/opencode /usr/local/bin/opencode; fi \
    && if [ -x /root/.opencode/bin/opencode ]; then ln -sf /root/.opencode/bin/opencode /usr/local/bin/opencode; fi \
    && curl -fsSL "https://caddyserver.com/api/download?os=linux&arch=amd64&version=${CADDY_VERSION}" -o /usr/local/bin/caddy \
    && chmod +x /usr/local/bin/caddy \
    && mkdir -p /etc/caddy /workspace /root/.config/opencode /etc/opencode \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY Caddyfile /etc/caddy/Caddyfile
COPY entrypoint.sh /entrypoint.sh
COPY boxapps-sync.sh /usr/local/bin/boxapps-sync.sh
COPY opencode.json /root/.config/opencode/opencode.json
COPY AGENTS.md /etc/opencode/AGENTS.template.md

RUN chmod +x /entrypoint.sh /usr/local/bin/boxapps-sync.sh

WORKDIR /workspace

EXPOSE 8080

ENTRYPOINT ["/entrypoint.sh"]
