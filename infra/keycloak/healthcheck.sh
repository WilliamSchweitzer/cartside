#!/bin/bash
# The upstream image has bash but no curl/wget. Compose bounds probe duration.
set -eu
exec 3<>/dev/tcp/127.0.0.1/9000
printf 'GET /health/ready HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3
IFS= read -r status <&3
[[ "$status" == *" 200 "* ]]
