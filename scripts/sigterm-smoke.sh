#!/usr/bin/env bash
# Local SIGTERM/SIGINT graceful-shutdown smoke test.
# Target: 127.0.0.1 ONLY (python stub). Never touches the real IAM service.
set -u
cd /home/z/my-project/iam-perf
export PATH=$PATH:/home/z/sdk/go/bin

# 1) Minimal stub IAM: 200 for everything (probe flow only — no auth needed).
python3 - <<'PY' &
import http.server, socketserver
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200); self.send_header("Content-Type","text/html"); self.end_headers()
        self.wfile.write(b"<html>ok</html>")
    def log_message(self, *a): pass
with socketserver.TCPServer(("127.0.0.1", 18099), H) as s:
    s.serve_forever()
PY
STUB_PID=$!
sleep 0.5

# 2) Config: probe flow against the stub, 60s window.
cat > /tmp/iamperf-sigtest.json <<'JSON'
{
  "schema_version": 1,
  "target": {"base_url": "http://127.0.0.1:18099", "allowed_paths": ["/login"]},
  "authorization": {"accepted": true, "scope_note": "localhost stub smoke test"},
  "scenario": {"name": "sigtest", "flow": "transport-probe", "model": "closed",
               "virtual_users": 2, "duration": "60s", "think_time": "200ms"},
  "report": {"output_dir": "/tmp/iamperf-sigtest-reports", "formats": ["json", "terminal"]},
  "log": {"level": "info", "format": "text"}
}
JSON
rm -rf /tmp/iamperf-sigtest-reports

# 3) Run, then SIGTERM mid-run (after the 3 s abort window).
./bin/iam-perf run -c /tmp/iamperf-sigtest.json --iam-accept-risk > /tmp/iamperf-sigtest.out 2>&1 &
CLI_PID=$!
sleep 4
kill -TERM $CLI_PID
wait $CLI_PID
EXIT_CODE=$?

echo "exit code: $EXIT_CODE (expect 130)"
grep -E "termination|login transactions|transactions/sec" /tmp/iamperf-sigtest.out | head -5
REPORT=$(ls /tmp/iamperf-sigtest-reports/*.json 2>/dev/null | head -1)
if [ -n "$REPORT" ]; then
  python3 -c "import json,sys; r=json.load(open('$REPORT')); print('report termination:', r['metadata']['termination'])"
else
  echo "FAIL: no report written"
fi

kill $STUB_PID 2>/dev/null
exit $((EXIT_CODE == 130 ? 0 : 1))
