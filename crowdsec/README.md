# SlipDrukka + CrowdSec

[English](#english) · [Українська](#українська)

## English

Optional. SlipDrukka has its own protection (bans, limits, WireGuard); this
is for servers that already run [CrowdSec](https://www.crowdsec.net/).

Two independent parts, both switched on in the panel under
**Settings → CrowdSec**:

1. **Log for CrowdSec** — the server writes every counted failed connection
   to the port for shops into `crowdsec.log` in its settings folder:

   ```
   2026-11-13T09:59:37+02:00 slipdrukka failed-connection src=192.0.2.77 reason=bad-handshake
   ```

   `reason` is `bad-handshake`, `unknown-code` or `expired-code`. Addresses of
   known shops (allow-list, tied codes, shops seen in the last 30 days) are
   never written, so CrowdSec cannot block a shop.
2. **Ask CrowdSec** — SlipDrukka pulls decisions from the CrowdSec Local API
   every 10 seconds, like any remediation component, and does not let banned
   addresses or ranges in. The same exemptions apply.

### Install the rules (Windows)

Copy the files from this folder:

| File | Where |
|---|---|
| `parsers/s00-raw/slipdrukka-raw.yaml` | `C:\ProgramData\CrowdSec\config\parsers\s00-raw\` |
| `parsers/s01-parse/slipdrukka-logs.yaml` | `C:\ProgramData\CrowdSec\config\parsers\s01-parse\` |
| `scenarios/slipdrukka-bf.yaml` | `C:\ProgramData\CrowdSec\config\scenarios\` |
| contents of `acquis-windows.yaml` | append to `C:\ProgramData\CrowdSec\config\acquis.yaml` (separated by `---`) |

Restart the CrowdSec service. `cscli parsers list` and `cscli scenarios list`
should show the `slipdrukka/…` items as `enabled,local`. Blocking itself is
done by the CrowdSec Windows Firewall component.

The scenario: 5 failed connections from one address within about 10 minutes
(`capacity: 4`, `leakspeed: 2m`) → ban for CrowdSec's default duration.

### Ask CrowdSec

On the machine with CrowdSec: `cscli bouncers add slipdrukka` — it prints the
key once. In the panel: tick "Do not let in addresses CrowdSec blocked",
enter the Local API address (`http://127.0.0.1:8080` by default) and the key.
The key is stored in `crowdsec.key` (readable by the service and
administrators only) and is never shown back or written to logs. The panel
shows the last sync and how many addresses and ranges are blocked.

### What was tested

On CrowdSec 1.8.2 in Linux (the official container, a separate throwaway
instance): both parsers load as local items, `cscli explain` shows every
line parsed down to the scenario, five lines produce a `ban` decision for
the address, the SlipDrukka server picks it up through the decisions stream
and drops it again after `cscli decisions delete`. Not tested: CrowdSec on
Windows itself (the paths above follow its documentation).

## Українська

Необов'язково. У SlipDrukka є свій захист (бани, ліміти, WireGuard); це для
серверів, де вже стоїть [CrowdSec](https://www.crowdsec.net/).

Дві незалежні частини, обидві вмикаються в панелі: **Налаштування →
CrowdSec**.

1. **Журнал для CrowdSec** — сервер пише кожне враховане невдале
   підключення до порту для магазинів у `crowdsec.log` у теці налаштувань:

   ```
   2026-11-13T09:59:37+02:00 slipdrukka failed-connection src=192.0.2.77 reason=bad-handshake
   ```

   `reason`: `bad-handshake`, `unknown-code` або `expired-code`. Адреси
   відомих магазинів (зі списку дозволених, прив'язані коди, магазини за
   останні 30 днів) сюди не пишуться ніколи, тож CrowdSec не заблокує
   магазин.
2. **Питати CrowdSec** — SlipDrukka кожні 10 секунд забирає рішення з
   локального API CrowdSec, як будь-який блокувальник, і не пускає заблоковані
   адреси й мережі. Винятки ті самі.

### Встановити правила (Windows)

Скопіюйте файли з цієї теки:

| Файл | Куди |
|---|---|
| `parsers/s00-raw/slipdrukka-raw.yaml` | `C:\ProgramData\CrowdSec\config\parsers\s00-raw\` |
| `parsers/s01-parse/slipdrukka-logs.yaml` | `C:\ProgramData\CrowdSec\config\parsers\s01-parse\` |
| `scenarios/slipdrukka-bf.yaml` | `C:\ProgramData\CrowdSec\config\scenarios\` |
| вміст `acquis-windows.yaml` | дописати в `C:\ProgramData\CrowdSec\config\acquis.yaml` (через `---`) |

Перезапустіть службу CrowdSec. `cscli parsers list` і `cscli scenarios list`
мають показати `slipdrukka/…` як `enabled,local`. Блокує сам модуль CrowdSec
для Windows Firewall.

Сценарій: 5 невдалих підключень з однієї адреси приблизно за 10 хвилин
(`capacity: 4`, `leakspeed: 2m`) → бан на стандартний термін CrowdSec.

### Питати CrowdSec

На машині з CrowdSec: `cscli bouncers add slipdrukka` — він один раз покаже
ключ. У панелі: галочка «Не пускати адреси, які заблокував CrowdSec», адреса
локального API (за замовчуванням `http://127.0.0.1:8080`) і ключ. Ключ
зберігається у `crowdsec.key` (читають лише служба й адміністратори), назад
не показується і в журнали не пишеться. Панель показує останню синхронізацію
і скільки адрес і мереж заблоковано.

### Що перевірено

На CrowdSec 1.8.2 під Linux (офіційний контейнер, окремий тимчасовий
екземпляр): обидва розбирачі завантажуються як локальні, `cscli explain`
показує, що кожен рядок проходить до сценарію, п'ять рядків дають рішення
`ban` для адреси, сервер SlipDrukka забирає його через потік рішень і
відпускає після `cscli decisions delete`. Не перевірено: сам CrowdSec під
Windows (шляхи вище — за його документацією).
