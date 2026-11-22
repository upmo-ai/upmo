# Installation

You need Docker and nothing else. Every command runs in a container that `compose.yml` defines.

## Run locally

```sh
make up               # start brain and postgres, brain listens on localhost:8080
curl -fsS localhost:8080/healthz
make down
```

To build, test and change the code, and to run the web dev server, see [Development](development.md).

## Next

To change settings, copy `.env.example` to `.env`. See [Configuration](configuration.md).

To run and back up a server, see [Operations](operations/README.md).
