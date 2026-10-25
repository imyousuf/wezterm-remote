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
from http.server import HTTPServer, SimpleHTTPRequestHandler
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1] / "web"
MOCK = r"""<script>
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
        if self.path.startswith('/api/panes'):
            panes = [dict(id=pane_id, mode=mode, cols=180, rows=70,
                          desktop_cols=180, desktop_rows=70,
                          title='Owned browser fixture', wez_pane_id=-1)
                     for pane_id, mode in [('fixture', 'cli'), ('shim-fixture', 'shim')]]
            data, kind = json.dumps(panes).encode(), 'application/json'
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
        browser('new-page', f'http://127.0.0.1:{server.server_port}/')
        browser('viewport', '390', '844')
        script = Path(__file__).with_name('mobile_browser_assertions.js').read_text()
        try:
            result = browser('eval', script, '--json')
        except subprocess.CalledProcessError as error:
            raise SystemExit(error.stderr) from None
        print(result.stdout)
        if not json.loads(result.stdout).get('result', {}).get('passed'):
            raise SystemExit(1)
    finally:
        server.shutdown()


if __name__ == '__main__':
    main()
