#!/usr/bin/env bash
# Regenerate all clients from OpenAPI specs.
# Requires: go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
SPEC_DIR="$ROOT_DIR/openapi"
GEN_DIR="$ROOT_DIR/internal/generated"

# Locate oapi-codegen: prefer $GOPATH/bin, fall back to PATH.
GOPATH_BIN="${GOPATH:-$HOME/go}/bin"
if command -v oapi-codegen &>/dev/null; then
    OAPI_CODEGEN=oapi-codegen
elif [ -x "$GOPATH_BIN/oapi-codegen" ]; then
    OAPI_CODEGEN="$GOPATH_BIN/oapi-codegen"
else
    echo "oapi-codegen not found. Install with:" >&2
    echo "  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest" >&2
    exit 1
fi

for spec in "$SPEC_DIR"/*.json; do
    name="$(basename "$spec" .json)"
    out_dir="$GEN_DIR/$name"
    mkdir -p "$out_dir"

    echo "Generating $name from $spec ..."
    if "$OAPI_CODEGEN" \
        -generate types,client \
        -package "$name" \
        -o "$out_dir/gen.go" \
        "$spec" 2>/dev/null; then
        echo "  OK: $name generated."
    else
        echo "  WARNING: generation failed for $name (spec may need OpenAPI 3.0 downgrade), keeping existing file."
    fi
done

echo "Done."
