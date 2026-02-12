FROM node:22-alpine

WORKDIR /app

# Copy package files
COPY package*.json ./

# Install dependencies
RUN npm ci --only=production

# Copy source and build
COPY tsconfig.json ./
COPY src/ ./src/
RUN npm install && npm run build && npm prune --production

# Run as non-root user
RUN adduser -D mcp
USER mcp

# MCP servers communicate via stdio
ENTRYPOINT ["node", "dist/index.js"]
