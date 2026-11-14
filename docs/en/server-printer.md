# Printer on the server

[Українська](../uk/server-printer.md) · [All pages](../README.md)

![The Printer on the server dialog](../img/server-printer.jpg)

## What it is for

Programs on the server (accounting, POS) print to ordinary Windows printers.
This dialog creates such a printer on the server for each shop printer. It
opens on its own after a printer is added, or from the "Create printer on the
server" button in a table row.

## How it works

* **Name programs will see** — the one the cashier picks in the print
  dialog. Starting with the shop name helps: "Station Receipt", "Station
  Labels".
* **Driver on the server:**
  * for "Receipt / label" — the printer **model's** driver (XP-80C,
    ZDesigner…). It renders the receipt, QR code and barcodes, so it must be
    installed on the server beforehand. The list shows the drivers already on
    the server;
  * for "Document" — the built-in "Microsoft XPS Document Writer v4" is
    filled in; nothing to install.
* "Create printer" — the server gets a port `SLIPDRUKKA_91xx`
  (`127.0.0.1:91xx`) and a Windows printer on it.

The `91xx` port is tied to the shop printer: even if the printer is removed
and shared again, the number stays the same and the queues on the server keep
working.

One shop printer can have several printers on the server with different
drivers — they all print to the same printer in the shop.

**Security:** ports `127.0.0.1:91xx` accept data only from the Windows print
spooler. A program that sends data to the port directly, bypassing the
Windows printer, is refused. Who may print is decided by the permissions of
the Windows printer on the server.

## Example

The stall's XP-58 receipt printer has been shared.

1. "Printer on the server" opens with the name filled in:
   "Market, stall 7 XP-58".
2. Driver on the server — "XP-58" (installed on the server earlier).
3. "Create printer". In the program on the server the cashier picks
   "Market, stall 7 XP-58" — and the receipt comes out at the stall.

## Notes

* ✎ next to the name renames the printer on the server. Programs remember
  printers by name, so after renaming it has to be picked again in them.
* The administrator installs the model driver on the server, as before: the
  program does not download drivers.
