FROM python:3.12-slim

WORKDIR /app

# Copy source first, then install
COPY pyproject.toml ./
COPY src/ ./src/

# Install the package
RUN pip install --no-cache-dir .

# Run as non-root user
RUN useradd -m mcp
USER mcp

# MCP servers communicate via stdio
ENTRYPOINT ["python", "-m", "bvbrc_mcp"]
