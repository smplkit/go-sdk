#!/usr/bin/env python3
"""Downgrade an OpenAPI 3.1.x spec to 3.0.3 for oapi-codegen compatibility.

Converts:
  - openapi version 3.1.x -> 3.0.3
  - anyOf with {"type": "null"} -> nullable: true on the remaining schema
  - Bare {"type": ["string", "null"]} -> {"type": "string", "nullable": true}

Writes the result to stdout so it can be piped or redirected.

Usage: python3 scripts/downgrade-spec.py openapi/flags.json > /tmp/flags-3.0.json
"""

import json
import sys


def downgrade_schema(obj):
    """Recursively convert 3.1-specific schema patterns to 3.0."""
    if not isinstance(obj, dict):
        if isinstance(obj, list):
            return [downgrade_schema(item) for item in obj]
        return obj

    result = {}
    for key, value in obj.items():
        if key == "anyOf":
            non_null = [s for s in value if s != {"type": "null"}]
            has_null = len(non_null) < len(value)
            if has_null and len(non_null) == 1:
                # anyOf [SomeType, {"type": "null"}] -> SomeType + nullable
                merged = downgrade_schema(non_null[0])
                merged["nullable"] = True
                result.update(merged)
                continue
            else:
                result[key] = [downgrade_schema(s) for s in value]
                continue

        if key == "type" and isinstance(value, list):
            # {"type": ["string", "null"]} -> {"type": "string", "nullable": true}
            types = [t for t in value if t != "null"]
            if len(types) == 1 and len(types) < len(value):
                result["type"] = types[0]
                result["nullable"] = True
                continue
            elif len(types) == 1:
                result["type"] = types[0]
                continue

        result[key] = downgrade_schema(value)

    return result


def downgrade_spec(spec):
    """Downgrade a full OpenAPI spec from 3.1.x to 3.0.3."""
    spec = downgrade_schema(spec)
    if spec.get("openapi", "").startswith("3.1"):
        spec["openapi"] = "3.0.3"
    return spec


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print(f"Usage: {sys.argv[0]} <spec.json>", file=sys.stderr)
        sys.exit(1)

    with open(sys.argv[1]) as f:
        spec = json.load(f)

    result = downgrade_spec(spec)
    json.dump(result, sys.stdout, indent=2)
    print()  # trailing newline
