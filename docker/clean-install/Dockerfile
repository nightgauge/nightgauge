# Clean-machine install gate (docs/RELEASE_CHECKLIST.md, gate G1).
#
# This image is deliberately what a first-time user has and nothing more:
# Ubuntu, VS Code from the official .deb, git, gh, Node, the `claude` CLI,
# and a non-root user with an EMPTY home. There is no Nightgauge binary, no
# ~/.nightgauge, no ~/.vscode/extensions and no development bundle anywhere on
# the filesystem. Everything Nightgauge needs must arrive inside the packaged
# .vsix, so the binary-resolution cascade (docs/GO_BINARY.md, #356) is
# exercised end to end instead of being short-circuited by host state.
#
# Built for the host's own platform (linux/arm64 on Apple Silicon Docker,
# linux/amd64 in GitHub Actions); the .vsix mounted at run time must match.
# Pinned by digest (multi-arch index); Dependabot's docker ecosystem keeps it
# current.
FROM ubuntu:26.04@sha256:2260313b31c8c011cd2eebe728008efac1b3982be73eb71348ea2648d2c0e09b

ARG NODE_MAJOR=22
ARG DEBIAN_FRONTEND=noninteractive

# Base toolchain plus the shared libraries VS Code (Electron) needs to start
# under Xvfb. `xvfb-run` is how the driver launches a real extension host.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg git jq unzip xz-utils python3 \
      xvfb xauth dbus-x11 xdotool \
      libasound2t64 libatk-bridge2.0-0 libatk1.0-0 libcairo2 libcups2 \
      libdrm2 libgbm1 libgtk-3-0 libnss3 libpango-1.0-0 libx11-xcb1 \
      libxcomposite1 libxdamage1 libxfixes3 libxkbcommon0 libxkbfile1 \
      libxrandr2 libxss1 libsecret-1-0 libgl1 xdg-utils fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

# GitHub CLI from GitHub's own apt repository.
RUN install -d -m 0755 /etc/apt/keyrings \
    && curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
         -o /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /etc/apt/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
         > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh \
    && rm -rf /var/lib/apt/lists/*

# Node from NodeSource — the extension host and the claude CLI both need it.
RUN curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# VS Code stable from the official .deb for this image's architecture.
RUN ARCH="$(dpkg --print-architecture)" \
    && curl -fsSL -o /tmp/code.deb "https://update.code.visualstudio.com/latest/linux-deb-${ARCH}/stable" \
    && apt-get update \
    && apt-get install -y --no-install-recommends /tmp/code.deb \
    && rm -f /tmp/code.deb && rm -rf /var/lib/apt/lists/* \
    && code --version --no-sandbox --user-data-dir=/tmp/code-version-probe \
    && rm -rf /tmp/code-version-probe

# The AI agent the extension drives by default (README § Before you start).
# Pinned so the gate exercises a known CLI, not whatever shipped overnight.
RUN npm install -g @anthropic-ai/claude-code@2.1.258 && npm cache clean --force

# A user with nothing in their home directory. Auth arrives at run time only:
# GH_TOKEN in the environment and either ANTHROPIC_API_KEY or a mounted
# ~/.claude/.credentials.json — the one thing the gate inherits from the host.
RUN useradd --create-home --shell /bin/bash tester
USER tester
WORKDIR /home/tester
ENV HOME=/home/tester

# The driver's entrypoint script is mounted alongside the VSIX at run time so
# the image never has to be rebuilt to change the walk.
ENTRYPOINT ["/bin/bash", "/harness/bin/run-in-container.sh"]
