#!/bin/sh
set -eu

VERSION="${VERSION:-1.0.0}"
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

make release VERSION="$VERSION"
file dist/stratum-migrate-linux-amd64 dist/stratum-migrate-linux-arm64
