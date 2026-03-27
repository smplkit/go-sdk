#!/usr/bin/env bash
# Regenerate all clients from OpenAPI specs.
# Requires: go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
SPEC_DIR="$ROOT_DIR/openapi"
GEN_DIR="$ROOT_DIR/internal/generated"

for spec in "$SPEC_DIR"/*.json; do
    name="$(basename "$spec" .json)"
    out_dir="$GEN_DIR/$name"
    mkdir -p "$out_dir"

    echo "Generating $name from $spec ..."
    oapi-codegen \
        -generate types \
        -package "$name" \
        -o "$out_dir/types.gen.go" \
        "$spec"
done

echo "Done."
