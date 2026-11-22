# syntax=docker/dockerfile:1

FROM python:3.12-slim@sha256:5e2bdb1b0d542d5039cffb8181c65930093949194a65c365dfb1b0fe13a4fa5b

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
