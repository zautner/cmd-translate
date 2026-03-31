#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

docker build -t cmd-translate .
echo "Image cmd-translate built."

# Optional: set RUN_EXAMPLES=1 and ensure LM Studio is listening on host:1234
if [[ "${RUN_EXAMPLES:-}" == "1" ]]; then
	docker run --rm -e LM_STUDIO_BASE_URL=http://host.docker.internal:1234/v1 \
		cmd-translate "find all go files modified in the last 7 days"
	echo "kill whatever is on port 8080" | docker run --rm -i \
		-e LM_STUDIO_BASE_URL=http://host.docker.internal:1234/v1 cmd-translate
fi
