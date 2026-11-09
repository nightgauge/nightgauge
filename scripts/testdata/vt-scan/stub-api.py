"""Offline stand-in for the VirusTotal v3 API, used only by the test suites.

Usage: python3 -I stub-api.py <fixture-dir> <state-dir>

Binds 127.0.0.1 on a free port and writes it to <state-dir>/port. Every request
is appended to <state-dir>/requests.log as "METHOD PATH key=<ok|bad|none>" so a
suite can count uploads and prove the key was sent as a header.

Like VirusTotal, the stub remembers every file it has analysed. GET
/files/{sha256} answers 404 NotFoundError for a file it does not know, and the
file object with `last_analysis_date`, `last_analysis_stats` and
`last_analysis_results` for one it does. An upload (POST /files) or a
reanalysis (POST /files/{sha256}/analyse) starts an analysis; it completes on
a later GET of the file (see pending-polls), which sets last_analysis_date to
a value above the previous one. A suite starts each scan against an empty
VirusTotal by writing a new value to <state-dir>/epoch; the stub forgets every
file when that value changes.

The verdict for an uploaded file is chosen by a marker in its bytes, so a fake
VSIX or tarball can carry a "detection" through extraction unchanged:

  VT-STUB-MALICIOUS    one engine reports malicious
  VT-STUB-SUSPICIOUS   one engine reports suspicious
  VT-STUB-YARA         one crowdsourced YARA rule matches
  VT-STUB-WINAPI-INQUEST    InQuest's Windows_API_Function rule matches
  VT-STUB-WINAPI-ELSEWHERE  the same rule name from another source matches
(YARA markers combine: each appends one match.)
  VT-STUB-AI-SUSP      Code insights verdict "suspicious"
  VT-STUB-AI-ABSENT    no crowdsourced_ai_results field
  VT-STUB-AI-ODD       crowdsourced_ai_results in an unknown shape

Run-wide state and failures are selected by files in <state-dir>:

  epoch                any value; when it changes the stub forgets every file
  seed                 lines "sha256 last_analysis_date path": files the stub
                       already knows, analysed at that epoch second, with the
                       verdict markers of the bytes at path
  few-engines          file objects list 3 engines instead of 70
  few-engines-once     the next file object lists 3 engines; the flag is then
                       removed
  fail-<endpoint>      answer that endpoint with HTTP 500
                       (endpoint: upload, upload_url, analyse, analysis, file)
  garble-<endpoint>    answer that endpoint with a body that is not JSON
  never-complete       analyses never complete
  pending-polls        holds N: each analysis stays incomplete for the first N
                       polls of it (GET /analyses/{id} or GET /files/{sha256}),
                       then completes
  quota                answer every request with HTTP 429
  wrong-sha            analyses and file objects name a different sha256
  upload-url           GET /files/upload_url serves this file's content
                       instead of the stub's own upload URL
"""

import hashlib
import json
import os
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

FIXTURES = sys.argv[1]
STATE = sys.argv[2]
EXPECTED_KEY = os.environ.get("STUB_VT_KEY", "")
UPLOADS = {}  # analysis id -> sha256
POLLS = {}  # analysis id -> polls answered so far
DATES = {}  # sha256 -> last_analysis_date of a file the stub knows
PENDING = {}  # sha256 -> polls of its running analysis answered so far
SEEDED = set()  # sha256s already loaded from <state-dir>/seed
EPOCH = [None]
ENGINES = 70


def flag(name):
    return os.path.exists(os.path.join(STATE, name))


def file_object(sha, content):
    with open(os.path.join(FIXTURES, "clean-file.json"), encoding="utf-8") as f:
        doc = json.load(f)
    attrs = doc["data"]["attributes"]
    doc["data"]["id"] = "0" * 64 if flag("wrong-sha") else sha
    stats, results = verdict(content)
    attrs["last_analysis_date"] = DATES[sha]
    attrs["last_analysis_stats"] = stats
    attrs["last_analysis_results"] = results
    attrs["crowdsourced_ai_results"][0]["id"] = sha
    yara = []
    if b"VT-STUB-YARA" in content:
        yara.append(
            {
                "rule_name": "Stub_Forced_Rule",
                "ruleset_name": "stub_rules",
                "source": "https://example.invalid/stub-rules",
            }
        )
    for marker, source in (
        (b"VT-STUB-WINAPI-INQUEST", "https://github.com/InQuest/yara-rules-vt"),
        (b"VT-STUB-WINAPI-ELSEWHERE", "https://example.invalid/fork-rules"),
    ):
        if marker in content:
            yara.append(
                {
                    "rule_name": "Windows_API_Function",
                    "ruleset_name": "Windows_API_Function",
                    "source": source,
                }
            )
    if yara:
        attrs["crowdsourced_yara_results"] = yara
    if b"VT-STUB-AI-SUSP" in content:
        attrs["crowdsourced_ai_results"][0]["verdict"] = "suspicious"
    if b"VT-STUB-AI-ABSENT" in content:
        del attrs["crowdsourced_ai_results"]
    if b"VT-STUB-AI-ODD" in content:
        attrs["crowdsourced_ai_results"] = {"verdict": "benign"}
    return doc


def pending_polls():
    if not flag("pending-polls"):
        return 0
    with open(os.path.join(STATE, "pending-polls"), encoding="utf-8") as f:
        return int(f.read().strip() or "0")


def verdict(content):
    """Engine stats and results for these bytes, from their markers."""
    count = ENGINES
    if flag("few-engines"):
        count = 3
    elif flag("few-engines-once"):
        count = 3
        os.remove(os.path.join(STATE, "few-engines-once"))
    names = ["StubEngineA", "StubEngineB"] + [
        f"StubEngine{n:02d}" for n in range(count - 2)
    ]
    results = {
        n: {"engine_name": n, "category": "undetected", "result": None}
        for n in names
    }
    stats = {"malicious": 0, "suspicious": 0, "undetected": count, "harmless": 0}
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
    stats["undetected"] -= stats["malicious"] + stats["suspicious"]
    return stats, results


def analysis_object(sha, content, polls=1):
    stats, results = verdict(content)
    queued = flag("never-complete") or polls <= pending_polls()
    status = "queued" if queued else "completed"
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


def sync_state():
    """Forget every file when <state-dir>/epoch changes; load new seed lines."""
    try:
        with open(os.path.join(STATE, "epoch"), encoding="utf-8") as f:
            epoch = f.read().strip()
    except OSError:
        epoch = None
    if epoch != EPOCH[0]:
        EPOCH[0] = epoch
        for table in (UPLOADS, POLLS, DATES, PENDING, CONTENTS):
            table.clear()
        SEEDED.clear()
    try:
        with open(os.path.join(STATE, "seed"), encoding="utf-8") as f:
            lines = f.read().splitlines()
    except OSError:
        lines = []
    for line in lines:
        sha, date, path = line.split(" ", 2)
        if sha in SEEDED:
            continue
        SEEDED.add(sha)
        with open(path, "rb") as f:
            CONTENTS[sha] = f.read()
        DATES[sha] = int(date)


def start_analysis(sha):
    PENDING[sha] = 0
    UPLOADS["an-" + sha] = sha


def poll_file(sha):
    """One GET of a file: completes its running analysis once it is due."""
    if sha not in PENDING:
        return
    PENDING[sha] += 1
    if flag("never-complete") or PENDING[sha] <= pending_polls():
        return
    del PENDING[sha]
    DATES[sha] = max(int(time.time()), DATES.get(sha, 0) + 1)


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
        start_analysis(sha)
        with open(os.path.join(STATE, "uploads.log"), "a", encoding="utf-8") as f:
            f.write(f"{endpoint} {sha} {len(content)}\n")
        self.answer("upload", 200, {"data": {"type": "analysis", "id": "an-" + sha}})

    def do_POST(self):
        sync_state()
        if self.record() != "ok":
            return self.answer(
                "auth", 401, {"error": {"code": "WrongCredentialsError"}}
            )
        if self.path == "/api/v3/files":
            return self.upload("small")
        if self.path == "/upload-large":
            return self.upload("large")
        if self.path.startswith("/api/v3/files/") and self.path.endswith("/analyse"):
            sha = self.path.split("/")[-2]
            if sha not in DATES:
                return self.answer("analyse", 404, {"error": {"code": "NotFoundError"}})
            start_analysis(sha)
            with open(os.path.join(STATE, "analyses.log"), "a", encoding="utf-8") as f:
                f.write(f"{sha}\n")
            return self.answer(
                "analyse", 200, {"data": {"type": "analysis", "id": "an-" + sha}}
            )
        return self.answer("none", 404, {"error": {"code": "NotFoundError"}})

    def do_GET(self):
        sync_state()
        if self.record() != "ok":
            return self.answer(
                "auth", 401, {"error": {"code": "WrongCredentialsError"}}
            )
        port = self.server.server_address[1]
        if self.path == "/api/v3/files/upload_url":
            url = f"http://127.0.0.1:{port}/upload-large"
            if flag("upload-url"):
                with open(os.path.join(STATE, "upload-url"), encoding="utf-8") as f:
                    url = f.read().strip()
            return self.answer("upload_url", 200, {"data": url})
        if self.path.startswith("/api/v3/analyses/"):
            aid = self.path.rsplit("/", 1)[-1]
            sha = UPLOADS.get(aid)
            if sha is None:
                return self.answer(
                    "analysis", 404, {"error": {"code": "NotFoundError"}}
                )
            POLLS[aid] = POLLS.get(aid, 0) + 1
            return self.answer(
                "analysis", 200, analysis_object(sha, CONTENTS[sha], POLLS[aid])
            )
        if self.path.startswith("/api/v3/files/"):
            sha = self.path.rsplit("/", 1)[-1]
            poll_file(sha)
            if sha not in DATES:
                return self.answer("file", 404, {"error": {"code": "NotFoundError"}})
            return self.answer("file", 200, file_object(sha, CONTENTS[sha]))
        return self.answer("none", 404, {"error": {"code": "NotFoundError"}})


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(os.path.join(STATE, "port.tmp"), "w", encoding="utf-8") as f:
    f.write(str(server.server_address[1]))
os.replace(os.path.join(STATE, "port.tmp"), os.path.join(STATE, "port"))
server.serve_forever()
