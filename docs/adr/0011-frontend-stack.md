# 11. Vite + React + Tailwind v4 + shadcn/ui

Status: accepted

## Context
The UI is a single-page admin app embedded in the Go binary. Turbopack is tied
to Next.js; DaisyUI and shadcn/ui are both Tailwind v4 compatible but overlap.

## Decision
- Vite for the SPA build, served by the Go binary (`-tags ui`).
- React + TypeScript, TanStack Query, React Router.
- Tailwind v4 with shadcn/ui only (accessible Radix primitives; strong table and
  form patterns), not DaisyUI, to keep one design system.
- System fonts: the server may run on networks with no internet access.

## Consequences
shadcn components are vendored into `web/src/components/ui` and owned by us.
