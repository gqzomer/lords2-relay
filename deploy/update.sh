#!/bin/sh
# Pull the newest image and restart the relay. Run on the VPS: /opt/lords2-relay/update.sh
# Note: a restart drops games in progress, so update when nobody is playing.
set -e
cd /opt/lords2-relay
docker compose pull
docker compose up -d
docker image prune -f
