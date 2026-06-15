# Domains and Launch

Use this guide when attaching public domains to a remote env or launching a primary client domain.

Public DNS remains the client's responsibility. `nf` attaches domains, prints DNS instructions, verifies readiness, and performs the primary cutover. It does not create or change public/client DNS records.

## Command Shape

```sh
nf domain list [site|site.target:env|remote]
nf domain add <site.target:env|remote> <domain> [domain...] [--primary] [--proxy cloudflare] [--setup avoid-downtime|quick] [--dry-run] [--execute --yes]
nf domain check <site.target:env|remote> [domain...] [--proxy cloudflare]
nf domain primary <site.target:env|remote> <domain> [--proxy cloudflare] [--setup avoid-downtime|quick] [--search-replace] [--force] [--wait-timeout 30m] [--wait-interval 30s] [--dry-run] [--execute --yes]
nf domain remove <site.target:env|remote> <domain> [domain...] [--delete-cert] [--dry-run] [--execute --yes]
```

`domain check` is read-only. `domain add`, `domain primary`, and `domain remove` are mutations and support dry-run.

## Domain Roles

`nf domain list` shows cached domain inventory keyed by full env IDs like `client.app1-linode:live`.

Roles:

* `primary`: the current public hostname for the env.
* `secondary`: a redirect or fallback hostname.

Management:

* `internal`: the generated provider hostname managed by `nf`.
* `external`: a client/public hostname.

The generated provider hostname is internal and is primary only until an external primary is set. After that, it remains listed as an internal secondary fallback.

## Choose the Launch Shape

Choose the primary hostname before the launch window:

```text
www-primary:       www.client.com, secondary client.com
apex-primary:      client.com, secondary www.client.com
subdomain-primary: reports.client.com only
```

Example commands:

```sh
nf domain add production www.client.com client.com --primary --dry-run
nf domain add production client.com www.client.com --primary --dry-run
nf domain add production reports.client.com --primary --dry-run
```

The first domain is primary when `--primary` is used. Later domains in the same command become secondaries.

## Pre-Launch Checklist

Before sending DNS instructions or entering a launch window:

* Confirm the repo remote points at the intended env with `nf remote show production`.
* Confirm the remote env ID directly with `nf site show <site.target:env>`.
* Confirm current domain inventory with `nf domain list production`.
* Decide whether launch should run against `www`, apex, or a subdomain.
* Decide whether `--search-replace` should run when making the primary transition.
* Confirm who controls DNS and who will make the change.
* Confirm whether Cloudflare is proxying the domain.
* Confirm whether basic auth, maintenance mode, cache, or redirect rules need coordination outside `nf`.
* Make sure rollback expectations are explicit before the window starts.

For high-pressure launches, write down the exact env ID and exact domains before running mutating commands.

## Add Domains and Send DNS Instructions

Preview the plan:

```sh
nf domain add production www.client.com client.com --primary --dry-run
```

Execute:

```sh
nf domain add production www.client.com client.com --primary --execute --yes
```

`nf domain add` attaches external domains and prints the DNS records the client must create. Kinsta records come from the Kinsta API. Linode records point the public hostnames at the target IPs.

For Kinsta, `--setup` accepts `avoid-downtime` or `quick` on add/primary:

```sh
nf domain add production www.client.com client.com --primary --setup avoid-downtime --dry-run
```

For Linode domains proxied through Cloudflare, include `--proxy cloudflare` consistently on add/check/primary/remove:

```sh
nf domain add production www.client.com client.com --primary --proxy cloudflare --dry-run
```

## Check Readiness

After the client says DNS has changed, or periodically if they may change DNS early, run:

```sh
nf domain check production www.client.com client.com
```

With no explicit domains, `domain check` checks cached external domains for the env:

```sh
nf domain check production
```

`nf domain check` reports provider/server readiness, expected public DNS, HTTP reachability, HTTPS certificate status, and whether the domain is already primary. It exits `0` when public checks are ready and `2` when DNS, HTTP, HTTPS, or provider readiness is still pending.

For Cloudflare-proxied Linode domains:

```sh
nf domain check production www.client.com client.com --proxy cloudflare
```

In Cloudflare mode, `nf` verifies public DNS resolves to Cloudflare IP ranges, skips public origin-IP matching, and checks direct Linode origin HTTPS with SNI so `Full (strict)` renewal problems are visible before Cloudflare starts returning edge errors.

## Launch the Primary Domain

Preview the primary transition:

```sh
nf domain primary production www.client.com --search-replace --dry-run
```

Execute:

```sh
nf domain primary production www.client.com --search-replace --execute --yes
```

`nf domain primary` launches one external domain as the primary public hostname for the env. By default it approves once up front, polls the same readiness checks as `nf domain check`, then launches immediately when checks pass without a second prompt.

Defaults:

```text
--wait-timeout 30m
--wait-interval 30s
```

Use `--force` only when you intentionally need to bypass readiness checks and launch immediately:

```sh
nf domain primary production www.client.com --force --execute --yes
```

For Cloudflare-proxied Linode domains:

```sh
nf domain primary production www.client.com --proxy cloudflare --search-replace --execute --yes
```

## Post-Launch Checks

Run readiness checks again:

```sh
nf domain check production www.client.com client.com
nf domain list production
```

Confirm manually:

* The selected public domain is primary.
* DNS still points where expected.
* HTTPS is valid in a browser.
* HTTP redirects to HTTPS as expected.
* The public domain does not redirect back to the generated internal hostname.
* Secondary domains redirect to the current primary. Linode secondaries use 302 redirects.
* WordPress URLs and obvious navigation paths use the intended hostname.

Do not change `nf.json` remotes from env IDs to public domains. Keep `production -> client.app1-linode:live` or `production -> client.kinsta:live` as the repo connection.

## Cloudflare Notes for Linode

Use Cloudflare SSL/TLS mode `Full (strict)`.

Cloudflare should still be configured with the Linode target IP as the origin record, but public DNS will return Cloudflare IPs.

In `--proxy cloudflare` mode, `nf` keeps per-domain Let's Encrypt origin certificates and renewal timers, waits for public DNS to resolve to Cloudflare IP ranges before certbot runs, verifies the ACME challenge path through Cloudflare, skips public origin-IP DNS matching, and checks direct origin HTTPS separately from Cloudflare edge HTTPS.

Keep Cloudflare WAF/cache/redirect rules from interfering with:

```text
/.well-known/acme-challenge/
```

If issuing the first cert while orange-clouded fails, temporarily use DNS-only or add a Cloudflare rule that bypasses redirects, cache, and security checks for that path, then re-run:

```sh
nf domain check production www.client.com client.com --proxy cloudflare
```

## Move or Retire Old Bindings

If the launch moved a domain from another target/env, retire the old binding after cutover. Include all domains that were attached to the old env:

```sh
nf domain remove client.app1-linode:live www.client.com client.com --proxy cloudflare --dry-run
nf domain remove client.app1-linode:live www.client.com client.com --proxy cloudflare --execute --yes
```

Use `--delete-cert` only after the rollback window if you also want to remove the old Let's Encrypt lineage. Otherwise certbot may later try to renew the old cert after DNS has moved, but keeping it briefly makes rollback safer.

Kinsta removal deletes non-primary domains from the Kinsta environment and refuses to remove the current primary domain.

## Rollback Window

Rollback depends on what changed. `nf` can manage domain bindings, but it does not control client DNS.

During the rollback window:

* Keep old cert lineages unless there is a reason to delete them.
* Keep the generated internal hostname available as fallback metadata.
* Keep exact old env IDs and domain commands visible.
* Coordinate DNS rollback with the DNS owner if the origin changed.

When in doubt, run read-only checks first:

```sh
nf domain list production
nf domain check production
```
