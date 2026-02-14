FROM python:3.11-slim

WORKDIR /app

# Install system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Copy package files
COPY pyproject.toml ./
COPY joshbot/ ./joshbot/
COPY skills/ ./skills/

# Install joshbot
RUN pip install --no-cache-dir .

# Create data directory
RUN mkdir -p /root/.joshbot

# Default to gateway mode
ENTRYPOINT ["joshbot"]
CMD ["gateway"]
