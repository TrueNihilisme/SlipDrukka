# Security

SlipDrukka faces the internet: the server waits for shops on one port. This
page says what is open, what protects it, and what does not.

## Reporting a vulnerability

Please report it privately: **Security → Report a vulnerability** on the
GitHub repository. Do not open a public issue for it. Say what an attacker
can do, which version, and how to reproduce it.

This is a project kept by one person in their spare time. A report is
answered as soon as possible; a fix for a confirmed issue goes out in the
next release, and KNOWN-ISSUES.md records it.

## Supported versions

Only the latest release gets fixes. Versions are 0.x: settings and the
protocol may still change between releases.

## What is open to the network

| Where | What listens | Reachable from |
|---|---|---|
| Server | port for shops, TCP (TLS) and UDP (WireGuard), default 7310 | the internet |
| Server | admin panel, default 7330 | this computer only (127.0.0.1), unless the administrator opens it to the network |
| Server | one local port per printer, 91xx | this computer only, and only the Windows print spooler |
| Shop | shop page, default 7320 | this computer only |

A shop opens nothing to the internet: it connects to the server itself.

## What protects it

* **Encryption both ways.** Shops talk to the server over TLS 1.2+ or over
  WireGuard built into the program. The connection code carries the
  server's key fingerprint, so a shop cannot be pointed at a fake server.
* **One code per shop.** Without a valid code nothing gets past the
  handshake. Unused codes expire (default 7 days). A shop's code can be
  limited to its own addresses.
* **Ban after guessing.** 5 failed connections in 10 minutes block the
  address (default 5 days). Allowed addresses and addresses of shops seen
  in the last 30 days are never banned. At most 4 unfinished handshakes per
  address and 64 in total.
* **Alerts.** A shop connecting from a new address shows up in the panel.
  Every change made in the panel is written to audit.log.
* **No administrator rights.** Both services run under their own Windows
  accounts (`NT SERVICE\SlipDrukkaServer`, `NT SERVICE\SlipDrukkaAgent`),
  not as SYSTEM. A broken service cannot take over Windows.
* **Panel sign-in.** Only an administrator of the server's Windows can sign
  in to the admin panel, and only on the server itself. The optional
  emergency password is accepted from other computers only when Settings
  allow it (off by default). The panel sends a Content-Security-Policy and
  refuses requests from other sites. The shop page has no password, so it
  cannot replace a working connection code.
* **Releases** are built by GitHub Actions from the tag, with actions pinned
  to commit SHAs; only the release step, run for tags, may write to the
  repository.
* **Optional:** CrowdSec rules (folder `crowdsec/`), and a firewall command
  in the panel (Settings) that narrows the program's rule to the shops port
  and the shops' addresses.

## What it does not protect

* **The firewall rule is open by default.** The services have no
  administrator rights, so they cannot change firewall rules. The program
  itself turns strangers away; for the firewall to do it, run the command
  from Settings. Giving the service the rights to do this itself was
  measured to work and rejected: it would let a broken service change the
  server's network settings.
* **Installers are not signed.** Windows shows "Unknown publisher". Check
  the file against `SHA256SUMS` from the release.
* **The connection code is the shop's key.** Whoever holds it can connect
  as that shop (limit it to the shop's addresses to narrow this). Codes end
  up in chats, on USB sticks and in verbose installer logs: if one may have
  leaked, issue a new one in the panel (More → Issue a new connection code); the old one
  stops working at once.
* **The admin panel has no HTTPS.** On the server itself this does not
  matter; from the network, the emergency password travels in clear text,
  which is why that is off by default.
* **Anyone signed in to the server can print** through its printer queues,
  as with any shared printer on that computer.
* **Builds are not reproducible yet** (KNOWN-ISSUES.md).
* **The code has not had an independent security audit.** It was reviewed
  by AI models (findings and fixes in KNOWN-ISSUES.md).

## Planned

* HTTPS for the admin panel when it is open to the network.
* One-time connection codes: after the first connection the shop gets a
  new long-term key, and the code itself stops working.
