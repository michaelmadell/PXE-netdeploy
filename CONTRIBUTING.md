# Contributing

Thanks for your interest in netdeploy. The project is young and has one
maintainer, so for now it takes **issues, not pull requests**.

## Reporting a bug

[Open an issue](https://github.com/michaelmadell/PXE-netdeploy/issues/new/choose)
with the bug template. The most useful reports include:

- the netdeploy version (shown under the menu in the admin UI) and
  the host OS;
- the client hardware or VM, its firmware boot mode (PXE or HTTP(S) Boot) and
  whether Secure Boot is on;
- what you did, what you expected, and what happened instead;
- the relevant part of the server log, from the time it happened:
  `C:\ProgramData\netdeploy\logs\netdeployd.log` on Windows, or
  `journalctl -u netdeploy` on Linux. The log doesn't contain secrets or
  tokens, but check it for host names and addresses you'd rather not share.

For a failed deployment, the deployment's own log (its page → Download) helps
too.

**Security problems:** don't open an issue; see [SECURITY.md](SECURITY.md).

## Suggesting a feature

Use the feature request template: describe the problem you want solved and
the hardware or setup involved, rather than a particular implementation.

## Pull requests

Not accepted yet: they'll be closed with a pointer here. If you've found a
fix, describe it in an issue; it may well be taken up. This will change once
the project settles; the notes below are how changes are made today, for
when it does.

## How the project works

- **Spec first:** API changes start in `api/openapi.yaml`, then `just gen`,
  then the implementation. Generated code is committed and CI checks it.
- **Decisions are recorded** as ADRs in `docs/adr/`; a reversal gets a new ADR.
- **Tested and untested are said out loud:** lab results go in
  `docs/spikes/`, with what was and wasn't checked.
- **Commit messages** say what changed and why, and carry no
  `Co-Authored-By` or other AI attribution trailers.

Building and running from source: [docs/dev/getting-started.md](docs/dev/getting-started.md).
The lab setup: [docs/dev/running-the-lab.md](docs/dev/running-the-lab.md).

## Code of conduct

Everyone taking part is expected to follow the
[Code of Conduct](CODE_OF_CONDUCT.md).
