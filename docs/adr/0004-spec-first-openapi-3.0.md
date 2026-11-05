# 4. Spec-first API with OpenAPI 3.0.3

Status: accepted

## Context
The API serves the web UI, automation, and the WinPE agent. Hand-maintained
types drift between Go and TypeScript.

## Decision
`api/openapi.yaml` is the source of truth. Go server interfaces come from
oapi-codegen (strict server, net/http routing); the TypeScript client from
openapi-typescript + openapi-fetch. Generated code is committed and CI fails on
drift.

The spec is OpenAPI **3.0.3**, not 3.1, because oapi-codegen supports only 3.0.
Nullability uses `nullable: true`; single-value discriminators use one-item enums.

## Consequences
- Revisit 3.1 when the Go generator supports it.
- Every API change is a spec change first, reviewed with its generated diff.
