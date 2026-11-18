"""Wait for the installed service to report the expected build version."""

import json
import os
import sys
import time
import urllib.error
import urllib.request


url, expected = sys.argv[1:]
deadline = time.monotonic() + float(os.environ.get('HEALTH_TIMEOUT_SECONDS', '45'))
last_error = 'Service did not answer'
while time.monotonic() < deadline:
    try:
        with urllib.request.urlopen(url, timeout=3) as response:
            health = json.load(response)
        if health.get('status') == 'ok' and health.get('version') == expected:
            print(json.dumps(health))
            break
        last_error = f'Unexpected service version/status: {health}'
    except (OSError, urllib.error.URLError, ValueError) as error:
        last_error = str(error)
    time.sleep(1)
else:
    raise SystemExit(last_error)
