FROM ox-test-base AS base

# Arguments for version control and cache invalidation
ARG AGENT_VERSION=latest
ARG CACHE_BUST=1

# Install codex CLI at specified version
# CACHE_BUST placement ensures agent installs are refreshed when needed
RUN echo "Cache bust: ${CACHE_BUST}" && \
    npm install -g @openai/codex@${AGENT_VERSION}

# Verify codex installation
RUN codex --version

# Copy Go module files first for better caching of dependencies
COPY go.mod go.sum /workspace/
RUN cd /workspace && go mod download

# Copy the rest of the ox source code
COPY . /workspace

# Build ox CLI inside the container
RUN cd /workspace && go build -o /usr/local/bin/ox ./cmd/ox

# Switch to test user for execution
USER test

# Default entrypoint for running tests
ENTRYPOINT ["go", "test"]