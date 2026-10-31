#!/usr/bin/env python3
"""Run isolated browser regressions with an already running ATR browser.

Run: python3 tests/mobile_browser.py --run
Opens a dedicated test tab, changes only that tab's viewport, and leaves it open.
All panes and sockets are fake; this test cannot send input to a real terminal.
"""
import argparse
import json
import subprocess
import threading
import time
from http.server import HTTPServer, SimpleHTTPRequestHandler
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1] / "web"
MOCK = r"""<script>
window.fixtureErrors = []; window.addEventListener('error', e => fixtureErrors.push(e.message));
window.testViewport = new EventTarget();
Object.assign(testViewport, {height: 844, width: 390, offsetTop: 0});
Object.defineProperty(window, 'visualViewport', {configurable: true, value: testViewport});
window.fixtureMessages = []; window.fixtureSockets = [];
window.WebSocket = class {
  static OPEN = 1;
  constructor() {
    this.readyState = 1; fixtureSockets.push(this);
    setTimeout(() => {
      this.onopen?.();
      const text = Array.from({length: 70}, (_, i) => `row ${i + 1} ` + 'terminal fixture '.repeat(15)).join('\r\n') + '\r\nPROMPT TAIL READY';
      this.onmessage?.({data: new TextEncoder().encode(text).buffer});
    }, 50);
  }
  send(data) { fixtureMessages.push(typeof data === 'string' ? data : new TextDecoder().decode(data)); }
  close() { this.readyState = 3; }
};
</script>"""


class Handler(SimpleHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if self.path.startswith('/api/files/') and 'missing-fixture' in self.path:
            self.send_error(404, 'Fixture file unavailable')
            return
        if self.path.startswith('/api/panes'):
            panes = [dict(id=pane_id, mode=mode, cols=180, rows=70,
                          desktop_cols=180, desktop_rows=70,
                          title='Owned browser fixture', wez_pane_id=-1)
                     for pane_id, mode in [('fixture', 'cli'), ('shim-fixture', 'shim')]]
            data, kind = json.dumps(panes).encode(), 'application/json'
        elif self.path.startswith('/api/files/roots'):
            data, kind = b'[{"name":"Fixture","path":"/owned-fixture"}]', 'application/json'
        elif self.path.startswith('/api/files/list'):
            data, kind = b'[{"name":"code.go","directory":false,"modified":"2026-01-01T00:00:00Z"},{"name":"image.png","directory":false,"modified":"2026-02-01T00:00:00Z"},{"name":"unsupported.bin","directory":false,"modified":"2026-02-01T00:00:00Z"}]', 'application/json'
        elif self.path.startswith('/api/files/download'):
            data, kind = b'fixture download', 'application/octet-stream'
        elif self.path.startswith('/api/files/read'):
            if 'unsupported.bin' in self.path:
                self.send_error(415, 'Unsupported fixture preview')
                return
            if 'image.png' in self.path:
                import base64
                data = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII=')
                kind = 'image/png'
            else:
                data, kind = b'package fixture\n// <script>must remain text</script>', 'text/plain; charset=utf-8'
        else:
            path = (ROOT / (self.path.split('?')[0].lstrip('/') or 'index.html')).resolve()
            if not path.is_relative_to(ROOT) or not path.is_file():
                self.send_error(404)
                return
            data = path.read_bytes()
            kind = {'.css': 'text/css', '.js': 'text/javascript'}.get(path.suffix, 'text/html')
            if path.name == 'index.html':
                data = data.replace(b'<script defer', MOCK.encode() + b'<script defer', 1)
        self.send_response(200)
        self.send_header('Content-Type', kind)
        self.end_headers()
        self.wfile.write(data)


def browser(*args):
    return subprocess.run(['atr', 'browser', *args], capture_output=True, text=True, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true', help='Open a dedicated fixture tab and run assertions')
    if not parser.parse_args().run:
        parser.error('Pass --run to open a dedicated browser test tab')
    server = HTTPServer(('127.0.0.1', 0), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        browser('new-page', 'about:blank')
        browser('viewport', '390', '844')
        browser('navigate', f'http://127.0.0.1:{server.server_port}/')
        script = Path(__file__).with_name('mobile_browser_assertions.js').read_text()
        browser('eval', 'window.fixtureCheckResult = null; ' + script + '.then(result => { window.fixtureCheckResult = result; }).catch(error => { window.fixtureCheckResult = {passed: false, error: error.message}; }); true', '--json')
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            result = browser('eval', 'window.fixtureCheckResult', '--json')
            parsed = json.loads(result.stdout).get('result')
            if parsed is not None:
                print(result.stdout)
                if not parsed.get('passed'):
                    raise SystemExit(1)
                break
            time.sleep(1)
        else:
            raise SystemExit('Browser assertions exceeded 120 seconds')
    finally:
        server.shutdown()


if __name__ == '__main__':
    main()
