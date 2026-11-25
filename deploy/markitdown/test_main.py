"""Behavior tests for the markitdown sidecar against a real uvicorn listener.

Run inside the sidecar image or python:3.12 with requirements.txt installed:
python -m unittest -v test_main
"""

import base64
import hashlib
import http.client
import os
import socket
import threading
import time
import unittest
import urllib.request

# Pinned in internal/platform/svcsig/svcsig_test.go too: Go signed this bearer.
VECTOR_KEY = "upmo_svc_markitdown_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"
VECTOR_UNIX = 1700000000
VECTOR_BODY = b"hello"
VECTOR_BEARER = "upmo_s2s_v2.markitdown.1700000000.AAECAwQFBgcICQoLDA0ODw.z-sGwNTWPwCw3h5aSL_nEk2K0WV6Mb41LsSZ8bIXzu0"
# Same request with the raw query b"q=\xe9": one non-ASCII byte, framed as one byte.
VECTOR_QUERY = b"q=\xe9"
VECTOR_QUERY_BEARER = "upmo_s2s_v2.markitdown.1700000000.AAECAwQFBgcICQoLDA0ODw.Vv5G-eD8auRbs5Z07w7O0v8ONT7GjI87rMLYDSaW9uo"

# The converter's markdown for the body "hello" with no file name or type.
HELLO_MARKDOWN = "hello"

os.environ["UPMO_MARKITDOWN_KEY"] = VECTOR_KEY
os.environ.pop("UPMO_MARKITDOWN_KEY_PREVIOUS", None)

import uvicorn  # noqa: E402

import main  # noqa: E402


def sign(key_text: str, audience: str, method: str, path: str, raw_query: bytes, body: bytes, unix: int) -> str:
    key = main.parse_key(key_text)
    nonce = base64.urlsafe_b64encode(os.urandom(16)).decode().rstrip("=")
    body_sum = hashlib.sha256(body).hexdigest().encode()
    mac = main.mac_over_v2(key, audience.encode(), unix, nonce.encode(), method.encode(), path.encode(), raw_query, body_sum)
    return f"upmo_s2s_v2.{audience}.{unix}.{nonce}.{base64.urlsafe_b64encode(mac).decode().rstrip('=')}"


class Listener:
    """main.app on a real socket, so requests go through uvicorn and HTTP."""

    def __enter__(self):
        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        self.url = f"http://127.0.0.1:{sock.getsockname()[1]}"
        self.server = uvicorn.Server(uvicorn.Config(main.app, log_level="warning"))
        self.thread = threading.Thread(target=self.server.run, kwargs={"sockets": [sock]}, daemon=True)
        self.thread.start()
        while not self.server.started:
            time.sleep(0.01)
        return self

    def __exit__(self, *exc):
        self.server.should_exit = True
        self.thread.join()

    def post(self, path: str, body: bytes, bearer: str | None) -> tuple[int, bytes]:
        headers = {"Content-Type": "application/octet-stream"}
        if bearer is not None:
            headers["Authorization"] = "Bearer " + bearer
        conn = http.client.HTTPConnection(self.url.removeprefix("http://"))
        try:
            try:
                conn.request("POST", path, body=body, headers=headers)
            except (BrokenPipeError, ConnectionResetError):
                pass  # the server answered a too-large body before reading it all
            res = conn.getresponse()
            return res.status, res.read()
        finally:
            conn.close()


class ConvertTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.listener = Listener().__enter__()

    @classmethod
    def tearDownClass(cls):
        cls.listener.__exit__(None, None, None)

    def test_no_bearer_is_401(self):
        status, body = self.listener.post("/convert", b"hello", None)
        self.assertEqual((status, body), (401, b'{"detail":"unauthorized"}'))

    def test_signed_hello_converts_once(self):
        bearer = sign(VECTOR_KEY, "markitdown", "POST", "/convert", b"", b"hello", int(time.time()))
        status, body = self.listener.post("/convert", b"hello", bearer)
        self.assertEqual(status, 200)
        self.assertEqual(body.decode(), '{"markdown":"' + HELLO_MARKDOWN + '"}')
        status, _ = self.listener.post("/convert", b"hello", bearer)
        self.assertEqual(status, 401, "a replayed bearer was accepted")

    def test_changed_query_is_401(self):
        bearer = sign(VECTOR_KEY, "markitdown", "POST", "/convert", b"a=1", b"hello", int(time.time()))
        status, _ = self.listener.post("/convert?a=2", b"hello", bearer)
        self.assertEqual(status, 401)
        status, body = self.listener.post("/convert?a=1", b"hello", bearer)
        self.assertEqual((status, body.decode()), (200, '{"markdown":"' + HELLO_MARKDOWN + '"}'))

    def test_swapped_body_is_401(self):
        bearer = sign(VECTOR_KEY, "markitdown", "POST", "/convert", b"", b"hello", int(time.time()))
        status, _ = self.listener.post("/convert", b"hellO", bearer)
        self.assertEqual(status, 401)

    def test_other_audience_is_401(self):
        bearer = sign(VECTOR_KEY, "gateway", "POST", "/convert", b"", b"hello", int(time.time()))
        status, _ = self.listener.post("/convert", b"hello", bearer)
        self.assertEqual(status, 401)

    def test_stale_bearer_is_401(self):
        bearer = sign(VECTOR_KEY, "markitdown", "POST", "/convert", b"", b"hello", int(time.time()) - 61)
        status, _ = self.listener.post("/convert", b"hello", bearer)
        self.assertEqual(status, 401)

    def test_over_the_cap_is_413(self):
        big = b"a" * (main.MAX_BODY_BYTES + 1)
        bearer = sign(VECTOR_KEY, "markitdown", "POST", "/convert", b"", big, int(time.time()))
        status, _ = self.listener.post("/convert", big, bearer)
        self.assertEqual(status, 413)

    def test_health_stays_open(self):
        with urllib.request.urlopen(self.listener.url + "/healthz") as res:
            self.assertEqual((res.status, res.read()), (200, b'{"status":"ok"}'))


class VerifyTest(unittest.TestCase):
    def test_go_vector_verifies(self):
        keys = [main.parse_key(VECTOR_KEY)]
        nonce = main.verify(keys, "POST", "/convert", b"", VECTOR_BODY, VECTOR_BEARER, VECTOR_UNIX)
        self.assertEqual(nonce, "AAECAwQFBgcICQoLDA0ODw")

    def test_go_raw_query_vector_verifies(self):
        keys = [main.parse_key(VECTOR_KEY)]
        nonce = main.verify(keys, "POST", "/convert", VECTOR_QUERY, VECTOR_BODY, VECTOR_QUERY_BEARER, VECTOR_UNIX)
        self.assertEqual(nonce, "AAECAwQFBgcICQoLDA0ODw")
        with self.assertRaises(main.Unauthorized):
            main.verify(keys, "POST", "/convert", "q=\xe9".encode(), VECTOR_BODY, VECTOR_QUERY_BEARER, VECTOR_UNIX)

    def test_go_vector_refuses_any_change(self):
        keys = [main.parse_key(VECTOR_KEY)]
        mac_flip = VECTOR_BEARER[:-1] + ("A" if VECTOR_BEARER[-1] != "A" else "B")
        for name, args in {
            "body": ("POST", "/convert", b"", b"hellO", VECTOR_BEARER, VECTOR_UNIX),
            "query": ("POST", "/convert", b"a=1", VECTOR_BODY, VECTOR_BEARER, VECTOR_UNIX),
            "path": ("POST", "/convert2", b"", VECTOR_BODY, VECTOR_BEARER, VECTOR_UNIX),
            "method": ("PUT", "/convert", b"", VECTOR_BODY, VECTOR_BEARER, VECTOR_UNIX),
            "mac": ("POST", "/convert", b"", VECTOR_BODY, mac_flip, VECTOR_UNIX),
            "stale": ("POST", "/convert", b"", VECTOR_BODY, VECTOR_BEARER, VECTOR_UNIX + 31),
            "future": ("POST", "/convert", b"", VECTOR_BODY, VECTOR_BEARER, VECTOR_UNIX - 31),
            "v1": ("POST", "/convert", b"", VECTOR_BODY, VECTOR_BEARER.replace("_v2.", "_v1.", 1), VECTOR_UNIX),
            "newline": ("POST", "/convert", b"", VECTOR_BODY, VECTOR_BEARER[:-5] + "\n" + VECTOR_BEARER[-5:], VECTOR_UNIX),
            "plus time": ("POST", "/convert", b"", VECTOR_BODY, VECTOR_BEARER.replace(".1700000000.", ".+1700000000.", 1), VECTOR_UNIX),
            "zero time": ("POST", "/convert", b"", VECTOR_BODY, VECTOR_BEARER.replace(".1700000000.", ".01700000000.", 1), VECTOR_UNIX),
            "padded mac": ("POST", "/convert", b"", VECTOR_BODY, VECTOR_BEARER + "=", VECTOR_UNIX),
        }.items():
            with self.subTest(name), self.assertRaises(main.Unauthorized):
                main.verify(keys, *args)

    def test_previous_key_verifies(self):
        other = main.parse_key("upmo_svc_markitdown_" + base64.b64encode(b"x" * 32).decode())
        nonce = main.verify([other, main.parse_key(VECTOR_KEY)], "POST", "/convert", b"", VECTOR_BODY, VECTOR_BEARER, VECTOR_UNIX)
        self.assertEqual(nonce, "AAECAwQFBgcICQoLDA0ODw")

    def test_load_keys_refuses_to_start(self):
        raw = base64.b64encode(b"k" * 32).decode()
        for env in (
            {},
            {"UPMO_MARKITDOWN_KEY": "upmo_svc_" + raw},
            {"UPMO_MARKITDOWN_KEY": "upmo_svc_gateway_" + raw},
            {"UPMO_MARKITDOWN_KEY": "upmo_svc_markitdown_" + base64.b64encode(b"k" * 31).decode()},
            {"UPMO_MARKITDOWN_KEY": VECTOR_KEY, "UPMO_MARKITDOWN_KEY_PREVIOUS": "upmo_mtr_" + raw},
        ):
            with self.subTest(env=env), self.assertRaises(RuntimeError):
                main.load_keys(env)
        self.assertEqual(len(main.load_keys({"UPMO_MARKITDOWN_KEY": VECTOR_KEY, "UPMO_MARKITDOWN_KEY_PREVIOUS": "upmo_svc_markitdown_" + raw})), 2)

    def test_load_keys_refuses_the_service_master(self):
        for env in (
            {"UPMO_MARKITDOWN_KEY": VECTOR_KEY, "UPMO_SERVICE_KEY": "upmo_svc_" + base64.b64encode(b"k" * 32).decode()},
            {"UPMO_MARKITDOWN_KEY": VECTOR_KEY, "UPMO_SERVICE_KEY": ""},
            {"UPMO_MARKITDOWN_KEY": VECTOR_KEY, "UPMO_SERVICE_KEY_PREVIOUS": "upmo_svc_x"},
            {"UPMO_MARKITDOWN_KEY": VECTOR_KEY, "UPMO_SERVICE_KEY_PREVIOUS": ""},
        ):
            with self.subTest(env=env), self.assertRaises(RuntimeError) as ctx:
                main.load_keys(env)
            self.assertEqual(str(ctx.exception), "markitdown: UPMO_SERVICE_KEY is not read; set UPMO_MARKITDOWN_KEY")

    def test_nonce_cache_forgets_after_retention(self):
        cache = main.NonceCache()
        self.assertTrue(cache.add("n", 1000.0))
        self.assertFalse(cache.add("n", 1059.0))
        self.assertTrue(cache.add("m", 1060.0))
        self.assertTrue(cache.add("n", 1061.0))


if __name__ == "__main__":
    unittest.main()
