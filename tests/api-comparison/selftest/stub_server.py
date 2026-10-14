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
      }
    }

`validate` emulates the legacy dispatchers' parameter-shape strictness:
"array" rejects non-list params (like legacy condenser `_strict_list`),
"object" rejects non-dict params, "any" accepts everything (like the Go
server's three-form params.go parsing).
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
