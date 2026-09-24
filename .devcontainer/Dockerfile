# Prebuilt VS Code devcontainer base with Node.js, npm, git, a non-root
# "node" user, and passwordless sudo already baked in. Using this image
# instead of layering the `node` devcontainer feature means the build
# never fetches node/npm/pnpm/yarn from the public npm registry — the
# toolchain ships in the image layers (pulled from MCR). Go is added via
# the `go` feature (see devcontainer.json); it installs from go.dev, not npm.
FROM mcr.microsoft.com/devcontainers/javascript-node:22-bookworm

ARG DEBIAN_FRONTEND=noninteractive

# System packages that don't come from npm:
#   - git-lfs: wiki sync routes binary assets through LFS on providers
#     that support it.
#   - chromium (+ fonts): headless PDF export drives Chromium via chromedp,
#     which finds it on PATH as `chromium`. Pulled from the Debian repos,
#     so the build needs no npm registry access at all. Fonts keep PDF and
#     Mermaid rendering legible.
#   - netcat-traditional: used by dev tooling / health probes.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        git-lfs \
        chromium \
        fonts-liberation \
        fonts-noto-core \
        netcat-traditional \
    && git lfs install --system \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*
