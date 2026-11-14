# How it fits together

[Українська](../uk/overview.md) · [All pages](../README.md)

SlipDrukka prints from programs on a Windows Server to printers in shops over
the internet. Nothing is set up on the shops' routers, and the receipt is
rendered by the driver on the server.

```
Server (Windows Server)                       Shop (Windows 10/11)
  program (accounting, POS…)                    slipdrukka-agent (service)
    → Windows printer with the model driver       → Windows printer
    → port 127.0.0.1:91xx                         → USB or network
    → slipdrukka-server (service)  ◄── tunnel ──  the shop dials in itself
```

Two programs:

* **Server** (`slipdrukka-server`) runs on the Windows Server next to the
  programs that print. It has an admin panel in the browser — everything in
  this wiki except the shop page.
* **Point** (`slipdrukka-agent`) runs on the shop computer. It connects to
  the server on its own and prints what the server sends. Its own page only
  shows the status.

## The path of one receipt

1. A cashier presses "Print" in a program on the server. The program prints
   to an ordinary Windows printer, for example "Station XP-80C".
2. The model driver on the server renders the receipt (text, QR, barcode) and
   sends it to port `127.0.0.1:9101`, which SlipDrukka listens on.
3. The server writes the receipt to disk first; only then does Windows hear
   "sent".
4. The server passes the receipt to the shop through the tunnel: WireGuard if
   it gets through, otherwise a regular encrypted TLS connection.
5. The point hands the receipt to the shop's Windows printer unchanged. The
   server removes it from its queue only after the point confirms printing.

If the shop has no internet, the receipt waits on the server and prints as
soon as the connection is back. Stale receipts are dropped (see
[Settings → Receipt queue](settings.md#receipt-queue)).

## Where to open what

| What | Where | Who |
|---|---|---|
| Server panel | `http://127.0.0.1:7330` on the server itself (over RDP) | administrator |
| Shop page | `http://127.0.0.1:7320` on the shop computer, or the icon next to the clock | anyone in the shop, view only |

## Example: the first shop in 10 minutes

1. On the server: install `slipdrukka-server-X.Y.Z.msi`, forward port 7310
   (TCP and UDP) on the server's router.
2. Open the panel and enter the public address in [Settings](settings.md),
   for example `relay.example.com:7310`.
3. [Add a shop](add-shop.md) — the panel gives an install command with the
   code.
4. Run that command on the shop computer. Within seconds the shop shows up
   in the panel as connected.
5. [Add a printer](add-printer.md) of the shop, then
   [create a printer on the server](server-printer.md) for it.
6. Print a test page from any program on the server.
