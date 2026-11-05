"""Offline stand-in for the VirusTotal v3 API, used only by the test suites.

Usage: python3 -I stub-api.py <fixture-dir> <state-dir>

Binds 127.0.0.1 on a free port and writes it to <state-dir>/port. Every request
is appended to <state-dir>/requests.log as "METHOD PATH key=<ok|bad|none>" so a
suite can count uploads and prove the key was sent as a header.

The verdict for an uploaded file is chosen by a marker in its bytes, so a fake
VSIX or tarball can carry a "detection" through extraction unchanged:

  VT-STUB-MALICIOUS    one engine reports malicious
  VT-STUB-SUSPICIOUS   one engine reports suspicious
  VT-STUB-YARA         one crowdsourced YARA rule matches
  VT-STUB-AI-SUSP      Code insights verdict "suspicious"
  VT-STUB-AI-ABSENT    no crowdsourced_ai_results field
  VT-STUB-AI-ODD       crowdsourced_ai_results in an unknown shape

Run-wide failures are selected by files in <state-dir>:

  fail-<endpoint>      answer that endpoint with HTTP 500
                       (endpoint: upload, upload_url, analysis, file)
  garble-<endpoint>    answer that endpoint with a body that is not JSON
  never-complete       analyses stay "queued" forever
  quota                answer every request with HTTP 429
  wrong-sha            analyses report a different sha256 than the upload
"""

import hashlib
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

FIXTURES = sys.argv[1]
STATE = sys.argv[2]
EXPECTED_KEY = os.environ.get("STUB_VT_KEY", "")
UPLOADS = {}  # analysis id -> sha256


def flag(name):
    return os.path.exists(os.path.join(STATE, name))


def file_object(sha, content):
    with open(os.path.join(FIXTURES, "clean-file.json"), encoding="utf-8") as f:
        doc = json.load(f)
    attrs = doc["data"]["attributes"]
    doc["data"]["id"] = sha
    attrs["crowdsourced_ai_results"][0]["id"] = sha
    if b"VT-STUB-YARA" in content:
        attrs["crowdsourced_yara_results"] = [
            {
                "rule_name": "Stub_Forced_Rule",
                "ruleset_name": "stub_rules",
                "source": "https://example.invalid/stub-rules",
            }
        ]
    if b"VT-STUB-AI-SUSP" in content:
        attrs["crowdsourced_ai_results"][0]["verdict"] = "suspicious"
    if b"VT-STUB-AI-ABSENT" in content:
        del attrs["crowdsourced_ai_results"]
    if b"VT-STUB-AI-ODD" in content:
        attrs["crowdsourced_ai_results"] = {"verdict": "benign"}
    return doc


def analysis_object(sha, content):
    stats = {"malicious": 0, "suspicious": 0, "undetected": 62, "harmless": 0}
    results = {
        "StubEngineA": {
            "engine_name": "StubEngineA",
            "category": "undetected",
            "result": None,
        },
        "StubEngineB": {
            "engine_name": "StubEngineB",
            "category": "undetected",
            "result": None,
        },
    }
    if b"VT-STUB-MALICIOUS" in content:
        stats["malicious"] = 1
        results["StubEngineA"] = {
            "engine_name": "StubEngineA",
            "category": "malicious",
            "result": "Stub.Forced.Detection",
        }
    if b"VT-STUB-SUSPICIOUS" in content:
        stats["suspicious"] = 1
        results["StubEngineB"] = {
            "engine_name": "StubEngineB",
            "category": "suspicious",
            "result": "Stub.Heuristic",
        }
    status = "queued" if flag("never-complete") else "completed"
    reported = "0" * 64 if flag("wrong-sha") else sha
    return {
        "data": {
            "type": "analysis",
            "id": "an-" + sha,
            "attributes": {"status": status, "stats": stats, "results": results},
        },
        "meta": {"file_info": {"sha256": reported}},
    }


CONTENTS = {}  # sha256 -> bytes


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        return

    def record(self):
        key = self.headers.get("x-apikey")
        state = "none" if key is None else ("ok" if key == EXPECTED_KEY else "bad")
        with open(os.path.join(STATE, "requests.log"), "a", encoding="utf-8") as f:
            f.write(f"{self.command} {self.path} key={state}\n")
        return state

    def answer(self, endpoint, code, doc):
        if flag("quota"):
            code, doc = (
                429,
                {"error": {"code": "QuotaExceededError", "message": "stub"}},
            )
        elif flag("fail-" + endpoint):
            code, doc = 500, {"error": {"code": "TransientError", "message": "stub"}}
        if flag("garble-" + endpoint) and code == 200:
            body = b"<html>not json</html>"
        else:
            body = json.dumps(doc).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def upload(self, endpoint):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        ctype = self.headers.get("Content-Type", "")
        boundary = ctype.split("boundary=", 1)[-1].strip('"').encode()
        content = b""
        for part in body.split(b"--" + boundary):
            head, sep, rest = part.partition(b"\r\n\r\n")
            if sep and b'name="file"' in head:
                content = rest[:-2] if rest.endswith(b"\r\n") else rest
        sha = hashlib.sha256(content).hexdigest()
        CONTENTS[sha] = content
        UPLOADS["an-" + sha] = sha
        with open(os.path.join(STATE, "uploads.log"), "a", encoding="utf-8") as f:
            f.write(f"{endpoint} {sha} {len(content)}\n")
        self.answer("upload", 200, {"data": {"type": "analysis", "id": "an-" + sha}})

    def do_POST(self):
        if self.record() != "ok":
            return self.answer(
                "auth", 401, {"error": {"code": "WrongCredentialsError"}}
            )
        if self.path == "/api/v3/files":
            return self.upload("small")
        if self.path == "/upload-large":
            return self.upload("large")
        return self.answer("none", 404, {"error": {"code": "NotFoundError"}})

    def do_GET(self):
        if self.record() != "ok":
            return self.answer(
                "auth", 401, {"error": {"code": "WrongCredentialsError"}}
            )
        port = self.server.server_address[1]
        if self.path == "/api/v3/files/upload_url":
            return self.answer(
                "upload_url", 200, {"data": f"http://127.0.0.1:{port}/upload-large"}
            )
        if self.path.startswith("/api/v3/analyses/"):
            aid = self.path.rsplit("/", 1)[-1]
            sha = UPLOADS.get(aid)
            if sha is None:
                return self.answer(
                    "analysis", 404, {"error": {"code": "NotFoundError"}}
                )
            return self.answer("analysis", 200, analysis_object(sha, CONTENTS[sha]))
        if self.path.startswith("/api/v3/files/"):
            sha = self.path.rsplit("/", 1)[-1]
            if sha not in CONTENTS:
                return self.answer("file", 404, {"error": {"code": "NotFoundError"}})
            return self.answer("file", 200, file_object(sha, CONTENTS[sha]))
        return self.answer("none", 404, {"error": {"code": "NotFoundError"}})


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(os.path.join(STATE, "port.tmp"), "w", encoding="utf-8") as f:
    f.write(str(server.server_address[1]))
os.replace(os.path.join(STATE, "port.tmp"), os.path.join(STATE, "port"))
server.serve_forever()
