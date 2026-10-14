#!/usr/bin/env python3
"""Stub JSON-RPC server for the comparator selftest (smoke) suite.

Serves scripted JSON-RPC responses from a JSON file so the comparison tool
can be exercised end-to-end without real hivemind services or a database.
This is test infrastructure only: it never talks to PostgreSQL or steemd.

Script file format (one entry per method):

    {
      "condenser_api.get_content": {
        "validate": "array",          # "array" | "object" | "any"
        "result": {...}               # served as the JSON-RPC result
      },
      "some.failing_method": {
        "error": {"code": 10, "message": "boom"}
      },
      "some.transport_failure": {
        "http_status": 500            # bare HTTP error, no JSON-RPC envelope
      },
      "some.non_envelope_200": {
        "raw_json": {"status": "ok"}  # HTTP 200 body WITHOUT a JSON-RPC
                                       # envelope (health-endpoint shape)
      },
      "some.redirect": {
        "redirect": "/other"          # 302 + Location; must not be followed
      }
    }

`validate` is a shape-level simulation of the legacy dispatchers'
parameter strictness (it checks only the params shape, not legacy
semantics): "array" rejects non-list params (like legacy condenser
`_strict_list`), "object" rejects non-dict params, "any" accepts
everything (like the Go server's three-form params.go parsing).
`http_status` responds with a bare HTTP error status to exercise the
comparator's transport-level failure handling. `raw_json` and
`redirect` are PR#420-review-r2 negative controls: they simulate a
URL mispointed at a health-check service (200 + non-envelope body)
and a redirecting endpoint respectively -- both must be judged red.
"""

import argparse
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SCRIPT = {}


class StubHandler(BaseHTTPRequestHandler):
    def do_GET(self):  # noqa: N802 (http.server API)
        # Minimal health endpoint so run_tests.sh health checks pass.
        payload = b'{"status": "ok"}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def do_POST(self):  # noqa: N802 (http.server API)
        try:
            length = int(self.headers.get("Content-Length", 0))
            request = json.loads(self.rfile.read(length) or b"{}")
        except (ValueError, json.JSONDecodeError):
            self._respond({"jsonrpc": "2.0", "id": None,
                           "error": {"code": -32700, "message": "parse error"}})
            return

        method = request.get("method")
        params = request.get("params", [])
        entry = SCRIPT.get(method)
        if entry is not None and "http_status" in entry:
            # Transport-level failure simulation: bare HTTP error status
            # with no JSON-RPC envelope (exercises the comparator's
            # fail-closed transport handling).
            self._respond_status(int(entry["http_status"]))
            return
        if entry is not None and "redirect" in entry:
            # Redirect simulation: a JSON-RPC endpoint must not redirect;
            # the comparator must fail closed instead of following.
            self._respond_redirect(str(entry["redirect"]))
            return
        if entry is not None and "raw_json" in entry:
            # Non-envelope 200 simulation (health-endpoint shape): served
            # verbatim, no JSON-RPC envelope. The comparator must reject
            # it as a malformed response even when both sides match.
            self._respond_raw_json(entry["raw_json"])
            return
        response = self._handle(method, params, entry, request.get("id"))
        self._respond(response)

    @staticmethod
    def _handle(method, params, entry, req_id):
        if entry is None:
            return {"jsonrpc": "2.0", "id": req_id,
                    "error": {"code": -32601,
                              "message": f"method not found: {method}"}}

        form = entry.get("validate", "any")
        shape_ok = (
            form == "any"
            or (form == "array" and isinstance(params, list))
            or (form == "object" and isinstance(params, dict))
        )
        if not shape_ok:
            return {"jsonrpc": "2.0", "id": req_id,
                    "error": {"code": -32602,
                              "message": f"invalid params: expected {form} "
                                         f"form for {method}"}}
        if "error" in entry:
            return {"jsonrpc": "2.0", "id": req_id, "error": entry["error"]}
        return {"jsonrpc": "2.0", "id": req_id, "result": entry.get("result")}

    def _respond(self, response):
        payload = json.dumps(response).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def _respond_status(self, status: int):
        payload = b"stub transport failure"
        self.send_response(status)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def _respond_redirect(self, location: str):
        # 302 without a body; only meaningful because the comparator
        # disables redirect following (a followed POST 302 would be
        # rewritten to GET by requests and hit do_GET instead).
        self.send_response(302)
        self.send_header("Location", location)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _respond_raw_json(self, body):
        payload = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *_args):
        pass  # keep selftest output quiet


def main():
    parser = argparse.ArgumentParser(description="Stub JSON-RPC server")
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--script", required=True,
                        help="JSON file with scripted responses")
    args = parser.parse_args()

    global SCRIPT
    with open(args.script) as handle:
        SCRIPT = json.load(handle)

    server = ThreadingHTTPServer(("127.0.0.1", args.port), StubHandler)
    print(f"stub listening on 127.0.0.1:{args.port} "
          f"({os.path.basename(args.script)}, {len(SCRIPT)} methods)",
          flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
