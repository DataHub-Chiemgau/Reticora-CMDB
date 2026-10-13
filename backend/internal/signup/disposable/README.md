# Wegwerf-Domains für den Self-Signup (TLC-01, E-33)

- `blocklist.conf` ist eine unveränderte Kopie von
  [`disposable-email-domains`](https://github.com/disposable-email-domains/disposable-email-domains)
  (`disposable_email_blocklist.conf`, CC0, siehe `LICENSE.txt`). Der Workflow
  `.github/workflows/disposable-domains.yml` aktualisiert sie monatlich per PR;
  von Hand wird sie nicht bearbeitet.
- `allowlist.conf` ist die eigene Allowlist: Diese Domains (und ihre
  Subdomains) gelten nie als Wegwerf-Domain.
- Betreiber ergänzen beides ohne Release über
  `RETICORA_SIGNUP_BLOCKED_DOMAINS` und `RETICORA_SIGNUP_ALLOWED_DOMAINS`
  (kommagetrennt). Die Allowlist hat Vorrang.
