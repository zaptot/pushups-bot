#!/usr/bin/env bash
set -euo pipefail

host="${DEPLOY_HOST:-root@72.56.89.122}"
cd "$(dirname "$0")"

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /tmp/pushbot .
(cd activity && npm ci --silent && npm run build)

ssh "$host" 'mkdir -p /opt/pushbot/activity'
scp -q /tmp/pushbot "$host":/opt/pushbot/pushbot.new
rsync -a --delete activity/dist/ "$host":/opt/pushbot/activity/dist/
ssh "$host" 'mv /opt/pushbot/pushbot.new /opt/pushbot/pushbot && systemctl restart pushbot && sleep 3 && systemctl --no-pager -n 5 status pushbot'
