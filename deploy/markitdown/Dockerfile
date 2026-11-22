# syntax=docker/dockerfile:1

FROM python:3.14-slim@sha256:9aaa474097d094040649f2162a627c0049b408f52138d980916788d80f04802f

# Pull in Debian security fixes the base image has not picked up yet.
RUN apt-get update \
  && apt-get upgrade -y --no-install-recommends \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY requirements.txt .
# requirements.txt is compiled from requirements.in with every hash:
# pip-compile --generate-hashes --strip-extras requirements.in
RUN pip install --no-cache-dir --require-hashes -r requirements.txt

COPY main.py .

USER nobody
EXPOSE 8000
ENTRYPOINT ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]
