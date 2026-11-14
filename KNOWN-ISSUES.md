# Known issues · Відомі проблеми

What we found while testing, what was fixed and what is still open.
Що знайшли під час тестів, що виправлено і що ще відкрите.

## Open · Відкрито

| | Issue | Notes |
|---|---|---|
| 1 | **Unsigned installers.** Windows shows "Unknown publisher" when the MSI is started. | Only a paid code-signing certificate removes it. Check the file against `SHA256SUMS` from the release. |
| 2 | **Setup wizard is English only.** | The panels are Ukrainian and English; the installer windows come from the MSI toolkit's stock dialogs. |
| 3 | **Builds are not byte-for-byte reproducible.** The MSI built by CI differs in size from one built on another machine. | Cause not investigated. Releases are built only by CI from the tag. |
| 4 | **WireGuard tests skip under `-race`.** gVisor's userspace network stack spins under the race detector (measured 745% CPU in `sleep.(*Sleeper).nextWaker`, 3% without). | Tunnel tests run without `-race` and in `internal/wgtun`; other end-to-end tests run over plain TLS under `-race`. |
| 5 | **Shop program runs on Windows only.** | macOS, Linux and Android shop programs are planned: same job protocol, a printing back-end per system. |
| 6 | **Document mode needs Windows at the shop** (XPS through the printer's own driver). | Receipt / label mode does not. |
| 7 | **One resilience test is flaky**: `TestResilienceStuckArrivalReleases` once failed in cleanup (`spool: directory not empty`), passed on rerun. | Probably the server still writes to the spool while the test removes it (assumption, not measured). Test-only. |

## Fixed · Виправлено

| Found | Problem | Fix |
|---|---|---|
| user's test of 0.15.0 | **English panels showed Ukrainian lines** wherever a name in «» was part of the text (shop status line, "Add a printer to …", "New code for …", delete prompts): the quotes were switched to English ones before the translation was looked up, so no pattern matched. | 0.15.1: quotes switched after the lookup; a general pattern no longer hides a more specific one; the shop status line is translated sentence by sentence. Checked: every screen in English mode, no Cyrillic left. |
| user's install of 0.14.1 | **After installing, nothing showed that the program works**: no shortcuts, no icon next to the clock until the next sign-in, two exes with unclear names, no program icon. | 0.15.0: shortcuts in Start and on the desktop (shop: SlipDrukka, server: SlipDrukka panel); the finish window starts the icon next to the clock and offers to open the shop page / admin panel; files renamed to `SlipDrukka.exe` (icon and shop page) and `SlipDrukka-service.exe` (the service, no window); program icon on the exes, shortcuts and in Apps. A silent install (`/qn`) starts the icon at the next sign-in. |
| user's test | **The panels had no navigation history**: Back in the browser left the panel, F5 lost the section, the shop page was one long page. | 0.15.0: each section has its own address (`#settings`, `#journal`, `#shop-…`; shop page `#events`, `#log`), Back closes an open window first; the shop page got section tabs. |
| resilience tests | **Receipts could swap.** A job got its order number later, in the background, and a later job for the same printer could be delivered while an earlier one was still arriving from Windows (measured: the later one printed first). | Order number taken when the connection arrives; a job waits while an earlier job of its printer is arriving; an arriving job silent for 30 s, or broken off, no longer holds the others. Tests: `TestResilienceOrderWhileArriving`, `…BrokenArrivalDoesNotBlock`, `…StuckArrivalReleases`, `…ReconnectStorm`. |
| CI on Linux (UTC) | **CrowdSec log time was `…Z` in UTC**, while the shipped parser was verified with `+02:00`. | The log always writes a numeric offset (`+00:00`). |
| `-race` run | **Deadlock**: the log asked for its language under the lock that the code writing to the log already held. | Language read without that lock. |
| `-race` run | **A new code could close the point's own new WireGuard tunnel**: the old tunnel was closed in the background. | Old tunnel closed at once, before reconnecting. |
| measurement | **Adding a shop made the server forget where the other points were** for up to 25 s: peers were replaced as a whole. | Peers updated one by one. |
| live test | **Two tunnels with one key** during a reconnect made the server answer the wrong one. | One tunnel per point, kept between connections. |
| live test | **After one WireGuard failure the point stayed on TLS for 30 min.** | First retry after 1 min, then every 30 min. |
| first install of 0.14.0 | **Removal of 0.14.0 failed** (error 1603 / 2762): the cleanup step was placed before `InstallInitialize` by the MSI tool although it was asked for "before StopServices". | 0.14.1 pins the step to 1950 (after StopServices, before DeleteServices). Installing 0.14.1 over 0.14.0 fixes an existing install; then it can be removed normally. |
| uninstall on test machines | **Uninstall left the UDP firewall rule and an empty program folder.** | `cleanup` removes every rule; the installer runs it on removal. |
| first start | **A code set before the program started tore down the first session** right after it opened. | That reconnect request is dropped at start. |

## Tested by hand · Перевірено вручну

* Windows Server 2019 (server) and Windows 11 (shops): receipts over WireGuard
  and over TLS, falling back when UDP is blocked on the shop and coming back
  when it is open, bans after 5 failed connections, a banned address let in by
  a valid code, upgrade from older versions with points reconnecting by
  themselves.
* CrowdSec 1.8.2 (Linux container, a separate throwaway instance): the shipped
  parsers and scenario load, five lines give a ban, the server picks it up and
  drops it. CrowdSec on Windows not tested.
