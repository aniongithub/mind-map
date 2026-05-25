FROM debian:bookworm-slim

ARG DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        curl \
        wget \
        pkg-config \
        libssl-dev \
        ca-certificates \
        git \
        git-lfs \
        netcat-traditional \
        sudo \
        # Playwright / Chromium runtime deps. Listed explicitly so we
        # don't have to invoke `npx playwright install-deps` at image
        # build time (which would pin us to a specific Playwright
        # version). These are the libraries Chromium dynamic-links
        # against in headless mode on Debian bookworm.
        libnss3 \
        libnspr4 \
        libatk1.0-0 \
        libatk-bridge2.0-0 \
        libcups2 \
        libdrm2 \
        libxkbcommon0 \
        libxcomposite1 \
        libxdamage1 \
        libxfixes3 \
        libxrandr2 \
        libgbm1 \
        libpango-1.0-0 \
        libcairo2 \
        libasound2 \
        libatspi2.0-0 \
        fonts-liberation \
        fonts-noto-color-emoji \
    && git lfs install \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user
ARG USERNAME=vscode
ARG USER_UID=1000
ARG USER_GID=$USER_UID
RUN groupadd --force --gid $USER_GID $USERNAME \
    && useradd --uid $USER_UID --gid $USER_GID -m $USERNAME -s /bin/bash || true \
    && echo "$USERNAME ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers.d/$USERNAME \
    && chmod 0440 /etc/sudoers.d/$USERNAME
