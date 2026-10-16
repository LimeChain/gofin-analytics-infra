#!/usr/bin/env python3
"""Exercise the real container against a synthetic upstream; no Google/PostHog access."""
import base64
import gzip
import http.client
import http.server
import json
import os
import subprocess
import threading
import time
import urllib.error
import urllib.request
import uuid


class Upstream(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        if self.path.startswith("/broken"):
            self.connection.shutdown(2)
            self.connection.close()
            return
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        status = 429 if self.path.startswith("/limited") else 202
        payload = json.dumps({
            "method": self.command,
            "path": self.path,
            "body": base64.b64encode(body).decode(),
            "headers": {key.lower(): value for key, value in self.headers.items()},
        }).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        if status == 429:
            self.send_header("Retry-After", "42")
        self.end_headers()
        self.wfile.write(payload)


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def main():
    image = os.environ.get("PROXY_IMAGE", "gofin-analytics-proxy:test")
    name = f"gofin-proxy-test-{uuid.uuid4().hex[:10]}"
    marker = "synthetic-private-marker"
    server = http.server.ThreadingHTTPServer(("0.0.0.0", 0), Upstream)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    upstream_port = server.server_port
    try:
        docker("run", "--detach", "--platform", "linux/amd64", "--name", name,
               "--add-host=host.docker.internal:host-gateway",
               "--publish", "127.0.0.1::8080",
               "--env", f"POSTHOG_UPSTREAM=http://host.docker.internal:{upstream_port}", image)
        port = docker("port", name, "8080/tcp").rsplit(":", 1)[1]
        origin = f"http://127.0.0.1:{port}"
        for _ in range(100):
            try:
                with urllib.request.urlopen(origin + "/healthz", timeout=1) as response:
                    assert response.status == 200 and response.read() == b"ok"
                break
            except (urllib.error.URLError, http.client.RemoteDisconnected):
                time.sleep(0.1)
        else:
            raise AssertionError("Proxy did not become ready")

        private_headers = {
            "Forwarded": "for=192.0.2.1",
            "X-Forwarded-For": "192.0.2.2",
            "X-Forwarded-Host": marker,
            "X-Forwarded-Proto": "https",
            "X-Real-IP": "192.0.2.3",
            "X-Client-IP": "192.0.2.4",
            "CF-Connecting-IP": "192.0.2.5",
            "True-Client-IP": "192.0.2.6",
            "Cookie": f"session={marker}",
            "Authorization": f"Bearer {marker}",
            "X-Cloud-Trace-Context": marker,
            "Traceparent": marker,
            "Tracestate": marker,
            "Baggage": marker,
        }
        # Deliberately malformed JSON must still be forwarded, as must compressed bytes.
        for body, encoding in [(b'{"event": "test"}', None),
                               (b"this is not JSON", None),
                               (gzip.compress(b'{"event": "compressed"}'), "gzip")]:
            headers = {**private_headers, "Content-Type": "application/json",
                       "User-Agent": marker}
            if encoding:
                headers["Content-Encoding"] = encoding
            path = "/i/v0/e/?test=" + marker
            request = urllib.request.Request(origin + path, data=body, headers=headers)
            with urllib.request.urlopen(request, timeout=10) as response:
                assert response.status == 202
                received = json.load(response)
            assert received["method"] == "POST" and received["path"] == path
            assert base64.b64decode(received["body"]) == body
            assert received["headers"]["content-type"] == "application/json"
            assert received["headers"].get("content-encoding") == encoding
            assert received["headers"]["user-agent"] == "gofin-analytics-proxy"
            assert received["headers"]["host"] == f"host.docker.internal:{upstream_port}"
            for key in private_headers:
                assert key.lower() not in received["headers"], key

        for path, expected in [("/limited", 429), ("/broken", 502)]:
            try:
                urllib.request.urlopen(urllib.request.Request(
                    origin + path + "?test=" + marker, data=b"test",
                    headers=private_headers), timeout=10)
                raise AssertionError(f"Expected {expected}")
            except urllib.error.HTTPError as response:
                assert response.code == expected
                if expected == 429:
                    assert response.headers["Retry-After"] == "42"
                    assert base64.b64decode(json.load(response)["body"]) == b"test"

        logs = subprocess.check_output(["docker", "logs", name],
                                       stderr=subprocess.STDOUT, text=True)
        assert marker not in logs, "Request data leaked into container logs"
        assert "192.0.2." not in logs, "Client IP leaked into container logs"
        print("PASS: health, opaque payloads, gzip, paths, privacy headers, 429, 502, request-log privacy")
    finally:
        subprocess.run(["docker", "rm", "--force", name], stdout=subprocess.DEVNULL,
                       stderr=subprocess.DEVNULL, check=False)
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    main()
