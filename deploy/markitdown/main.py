"""HTTP wrapper around markitdown: one file to markdown per request.

Internal-only sidecar, brain is the sole caller. POST /convert needs a v2
service bearer for the markitdown audience, signed with UPMO_MARKITDOWN_KEY
(or UPMO_MARKITDOWN_KEY_PREVIOUS during rotation). The check matches Go's
svcsig.VerifyRequestIndex byte for byte. /healthz stays open.
"""

import base64
import binascii
import hashlib
import hmac
import io
import os
import re
import threading
import time

from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import JSONResponse
from markitdown import MarkItDown, StreamInfo

AUDIENCE = "markitdown"
KEY_PREFIX = "upmo_svc_markitdown_"
BEARER_PREFIX = "upmo_s2s_v2."
KNOWN_AUDIENCES = {"gateway", "sandboxd", "markitdown", "provision"}
TOLERANCE_SECONDS = 30
# Twice the tolerance, so a swept nonce belongs to a bearer that can no longer verify.
NONCE_RETENTION_SECONDS = 2 * TOLERANCE_SECONDS
INT64_MIN, INT64_MAX = -(2**63), 2**63 - 1

_STD_B64 = re.compile(r"[A-Za-z0-9+/]*")
_URL_B64 = re.compile(r"[A-Za-z0-9_-]+")
_UNIX = re.compile(r"0|-?[1-9][0-9]*")


class Unauthorized(Exception):
    """A bearer that does not verify. The reason is never sent to the caller."""


def parse_key(secret: str) -> bytes:
    """Accept upmo_svc_markitdown_ plus base64 of exactly 32 bytes, padded or not."""
    if not secret.startswith(KEY_PREFIX):
        raise ValueError("key is not upmo_svc_markitdown_ followed by base64 of 32 bytes")
    text = secret[len(KEY_PREFIX):].rstrip("=")
    if not _STD_B64.fullmatch(text) or len(text) % 4 == 1:
        raise ValueError("key is not upmo_svc_markitdown_ followed by base64 of 32 bytes")
    raw = base64.b64decode(text + "=" * (-len(text) % 4))
    if len(raw) != 32:
        raise ValueError("key is not upmo_svc_markitdown_ followed by base64 of 32 bytes")
    return raw


def load_keys(environ) -> list[bytes]:
    """The current key, then the previous one when set. Refuses to start without a key."""
    # Set at all, even empty, the retired master must not sit next to the audience key.
    if "UPMO_SERVICE_KEY" in environ or "UPMO_SERVICE_KEY_PREVIOUS" in environ:
        raise RuntimeError("markitdown: UPMO_SERVICE_KEY is not read; set UPMO_MARKITDOWN_KEY")
    current = environ.get("UPMO_MARKITDOWN_KEY", "")
    previous = environ.get("UPMO_MARKITDOWN_KEY_PREVIOUS", "")
    if not current:
        raise RuntimeError("markitdown: UPMO_MARKITDOWN_KEY is required")
    keys = []
    for name, value in (("UPMO_MARKITDOWN_KEY", current), ("UPMO_MARKITDOWN_KEY_PREVIOUS", previous)):
        if not value:
            continue
        try:
            keys.append(parse_key(value))
        except ValueError as exc:
            raise RuntimeError(f"markitdown: {name}: {exc}") from None
    return keys


def _strict_url_b64(text: str) -> bytes:
    # Go's RawURLEncoding.Strict(): no padding and no spare bits, so one value has one spelling.
    if not _URL_B64.fullmatch(text):
        raise Unauthorized()
    try:
        raw = base64.urlsafe_b64decode(text + "=" * (-len(text) % 4))
    except (binascii.Error, ValueError):
        raise Unauthorized() from None
    if base64.urlsafe_b64encode(raw).decode().rstrip("=") != text:
        raise Unauthorized()
    return raw


def mac_over_v2(key: bytes, audience: bytes, unix: int, nonce: bytes, method: bytes, path: bytes, raw_query: bytes, body_sum: bytes) -> bytes:
    """Each field is length prefixed in bytes, the same bytes Go's macOverV2 writes."""
    h = hmac.new(key, digestmod=hashlib.sha256)
    for data in (b"v2", audience, str(unix).encode(), nonce, method, path, raw_query, body_sum):
        h.update(str(len(data)).encode() + b":" + data)
    return h.digest()


def verify(keys: list[bytes], method: str, path: str, raw_query: bytes, body: bytes, bearer: str, now: float) -> str:
    """Return the bearer's nonce when it verifies for this request, else raise Unauthorized."""
    if not keys or not method or not path:
        raise Unauthorized()
    # The base64 decoder skips line breaks, so they would give a second spelling of one token.
    if "\r" in bearer or "\n" in bearer or not bearer.startswith(BEARER_PREFIX):
        raise Unauthorized()
    parts = bearer[len(BEARER_PREFIX):].split(".", 3)
    if len(parts) != 4:
        raise Unauthorized()
    audience, unix_text, nonce, mac_text = parts
    if audience not in KNOWN_AUDIENCES or not nonce or not mac_text or "." in mac_text:
        raise Unauthorized()
    if not _UNIX.fullmatch(unix_text):
        raise Unauthorized()
    unix = int(unix_text)
    if not INT64_MIN <= unix <= INT64_MAX:
        raise Unauthorized()
    got = _strict_url_b64(mac_text)
    if len(got) != 32 or len(_strict_url_b64(nonce)) != 16:
        raise Unauthorized()
    if audience != AUDIENCE:
        raise Unauthorized()
    body_sum = hashlib.sha256(body).hexdigest().encode()
    fields = (audience.encode(), unix, nonce.encode(), method.encode(), path.encode(), raw_query, body_sum)
    matched = False
    for key in keys:
        if hmac.compare_digest(got, mac_over_v2(key, *fields)):
            matched = True
    if not matched:
        raise Unauthorized()
    if abs(now - unix) > TOLERANCE_SECONDS:
        raise Unauthorized()
    return nonce


class NonceCache:
    """Nonces of verified bearers, kept for NONCE_RETENTION_SECONDS."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._seen: dict[str, float] = {}
        self._swept = 0.0

    def add(self, nonce: str, now: float) -> bool:
        """Record nonce and report whether it was new."""
        with self._lock:
            if now - self._swept >= 1:
                self._seen = {n: at for n, at in self._seen.items() if now - at < NONCE_RETENTION_SECONDS}
                self._swept = now
            if nonce in self._seen:
                return False
            self._seen[nonce] = now
            return True


KEYS = load_keys(os.environ)
NONCES = NonceCache()

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
    bearer = request.headers.get("authorization", "")
    if not bearer.startswith("Bearer "):
        raise HTTPException(status_code=401, detail="unauthorized")
    body = await read_capped_body(request)
    now = time.time()
    try:
        nonce = verify(KEYS, request.method, request.scope["path"], request.scope.get("query_string", b""), body, bearer[len("Bearer "):], now)
    except Unauthorized:
        raise HTTPException(status_code=401, detail="unauthorized") from None
    if not NONCES.add(nonce, now):
        raise HTTPException(status_code=401, detail="unauthorized")
    if not body:
        raise HTTPException(status_code=400, detail="empty request body")

    stream_info = StreamInfo(filename=x_filename, mimetype=x_mimetype)
    try:
        result = converter.convert_stream(io.BytesIO(body), stream_info=stream_info)
    except Exception as exc:
        raise HTTPException(status_code=422, detail=f"conversion failed: {exc}") from exc

    return JSONResponse({"markdown": result.markdown})
