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

DOWNGRADE="$SCRIPT_DIR/downgrade-spec.py"

for spec in "$SPEC_DIR"/*.json; do
    name="$(basename "$spec" .json)"
    out_dir="$GEN_DIR/$name"
    mkdir -p "$out_dir"

    # oapi-codegen doesn't support OpenAPI 3.1. Downgrade to 3.0 if needed.
    tmp_spec="$spec"
    if python3 -c "import json,sys; sys.exit(0 if json.load(open('$spec')).get('openapi','').startswith('3.1') else 1)" 2>/dev/null; then
        tmp_spec=$(mktemp)
        python3 "$DOWNGRADE" "$spec" > "$tmp_spec"
        echo "  Downgraded $name spec from 3.1 to 3.0.3"
    fi

    echo "Generating $name from $spec ..."
    if "$OAPI_CODEGEN" \
        -generate types,client \
        -package "$name" \
        -o "$out_dir/gen.go" \
        "$tmp_spec" 2>/dev/null; then
        echo "  OK: $name generated."
    else
        echo "  WARNING: generation failed for $name, keeping existing file."
    fi

    # Clean up temp file if we created one
    [ "$tmp_spec" != "$spec" ] && rm -f "$tmp_spec"
done

echo "Done."
