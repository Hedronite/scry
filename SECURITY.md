# Security policy

## Reporting

Report vulnerabilities privately through GitHub: **Security → Report a vulnerability** on this repository. Do not open a public issue for a suspected vulnerability. Include the version (`scry version`), what you did, and what you saw.

## What counts

Scry's promise is that it is inspect-only. These are all security bugs:

- any request that is not an allowlisted `GET` leaving the process;
- a `watch` or any long-lived stream;
- running an exec or auth-provider plugin, or honouring `insecure-skip-tls-verify`;
- a kubeconfig path, server URL, token, certificate or file content appearing in output, an error, or a log.

## Supported versions

Only the latest tagged release.
