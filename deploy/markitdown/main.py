"""HTTP wrapper around markitdown: one file to markdown per request.

Internal-only sidecar — brain is the sole caller. No auth: never publish
this port outside the compose network.
"""

import io

from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import JSONResponse
from markitdown import MarkItDown, StreamInfo

app = FastAPI()
converter = MarkItDown(enable_plugins=False)

MAX_BODY_BYTES = 32 * 1024 * 1024


async def read_capped_body(request: Request) -> bytes:
    declared = request.headers.get("content-length")
    if declared is not None:
        try:
            if int(declared) > MAX_BODY_BYTES:
                raise HTTPException(status_code=413, detail="request body too large")
        except ValueError:
            raise HTTPException(status_code=400, detail="invalid content-length") from None

    chunks: list[bytes] = []
    total = 0
    async for chunk in request.stream():
        total += len(chunk)
        if total > MAX_BODY_BYTES:
            raise HTTPException(status_code=413, detail="request body too large")
        chunks.append(chunk)
    return b"".join(chunks)


@app.get("/healthz")
def healthz():
    return {"status": "ok"}


@app.post("/convert")
async def convert(
    request: Request,
    x_filename: str | None = Header(default=None),
    x_mimetype: str | None = Header(default=None),
):
    body = await read_capped_body(request)
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    stream_info = StreamInfo(filename=x_filename, mimetype=x_mimetype)
    try:
        result = converter.convert_stream(io.BytesIO(body), stream_info=stream_info)
    except Exception as exc:
        raise HTTPException(status_code=422, detail=f"conversion failed: {exc}") from exc

    return JSONResponse({"markdown": result.markdown})
