# Sandbox images. Language toolchains are not baked in; they install on
# first use into the /opt/mise volume. Build from the repository root:
# docker build -f deploy/sandbox/Dockerfile --target cli .

FROM debian:bookworm-slim AS base
RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates git \
  && rm -rf /var/lib/apt/lists/*
ENV MISE_DATA_DIR=/opt/mise
RUN mkdir -p /opt/mise
VOLUME /opt/mise

# Records the engine names this image supports. The programs themselves are
# not downloaded here.
FROM base AS cli
RUN mkdir -p /opt/engines
COPY deploy/sandbox/engines.txt /opt/engines/engines.txt
