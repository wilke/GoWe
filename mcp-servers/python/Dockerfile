FROM python:3.12-slim

WORKDIR /app

# Install dependencies
COPY pyproject.toml ./
RUN pip install --no-cache-dir -e .

# Copy source
COPY src/ ./src/

# Run as non-root user
RUN useradd -m mcp
USER mcp

# MCP servers communicate via stdio
ENTRYPOINT ["python", "-m", "bvbrc_mcp"]
