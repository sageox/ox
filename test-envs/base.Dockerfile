FROM golang:1.24-bookworm

# Install essential tools for test environment
RUN apt-get update && apt-get install -y \
    git \
    curl \
    jq \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 22 for agent CLIs that use npm
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# Create test user with home directory
ENV HOME=/home/test
RUN useradd -m -d /home/test test

# Set working directory for test execution
WORKDIR /workspace

# Verify installations
RUN go version && node --version && npm --version && git --version