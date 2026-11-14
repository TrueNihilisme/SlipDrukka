#!/bin/sh
# Builds the three Windows programs into dist/.
#   ./build.sh            version from VERSION file
#   VERSION=0.2.0 ./build.sh
set -eu
cd "$(dirname "$0")"
VERSION="${VERSION:-$(cat VERSION)}"
OUT=dist
rm -rf "$OUT" && mkdir -p "$OUT"
# Version resource (File/Product version, name) into each exe: Windows shows
# it under Properties > Details, and Windows Installer compares versions
# instead of treating our files as unversioned "user data".
WINRES="go run github.com/tc-hib/go-winres@v0.3.3"
res() { # dir manifest product description
  $WINRES simply --arch amd64 --manifest "$2" --out "$1/rsrc" \
    --product-version "$VERSION.0" --file-version "$VERSION.0" \
    --product-name "$3" --file-description "$4" --original-filename "$(basename "$1").exe"
}
res cmd/slipdrukka-server cli "SlipDrukka Server" "SlipDrukka Server service"
res cmd/slipdrukka-agent  cli "SlipDrukka Agent"  "SlipDrukka Agent service"
res cmd/slipdrukka-tray   gui "SlipDrukka Agent"  "SlipDrukka tray icon"
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
LD="-s -w -X main.version=$VERSION"
go build -trimpath -ldflags "$LD" -o "$OUT/slipdrukka-server.exe" ./cmd/slipdrukka-server
go build -trimpath -ldflags "$LD" -o "$OUT/slipdrukka-agent.exe" ./cmd/slipdrukka-agent
go build -trimpath -ldflags "$LD -H=windowsgui" -o "$OUT/slipdrukka-tray.exe" ./cmd/slipdrukka-tray
# MSI packages (msitools/wixl; brew install msitools). See installer/*.wxs.
if command -v wixl >/dev/null 2>&1; then
  wixl -a x64 --ext ui -D Version="$VERSION" -o "$OUT/slipdrukka-server-$VERSION.msi" installer/server.wxs installer/ui.wxs
  wixl -a x64 --ext ui -D Version="$VERSION" -o "$OUT/slipdrukka-agent-$VERSION.msi" installer/agent.wxs installer/ui.wxs
  # The connection code passed as CODE=... is written to the registry for the
  # service to pick up. Close that key to everyone but SYSTEM and
  # Administrators. wixl has no element for it, so the table is added here
  # (Windows Installer 5: MsiLockPermissionsEx, permissions as SDDL).
  REG=$(msiinfo export "$OUT/slipdrukka-agent-$VERSION.msi" Registry | awk -F'\t' '$4=="AgentCode"{print $1}')
  [ -n "$REG" ] || { echo "AgentCode registry row not found in the agent MSI" >&2; exit 1; }
  printf 'MsiLockPermissionsEx\tLockObject\tTable\tSDDLText\tCondition\r\ns72\ts72\ts32\ts0\tS255\r\nMsiLockPermissionsEx\tMsiLockPermissionsEx\r\nLockAgentCode\t%s\tRegistry\tD:PAI(A;CI;KA;;;SY)(A;CI;KA;;;BA)\t\r\n' "$REG" > "$OUT/MsiLockPermissionsEx.idt"
  msibuild "$OUT/slipdrukka-agent-$VERSION.msi" -i "$OUT/MsiLockPermissionsEx.idt"
  rm "$OUT/MsiLockPermissionsEx.idt"
else
  echo "wixl not found — MSI packages skipped (brew install msitools)" >&2
fi
# Checksums of what is published: the two installers.
cd "$OUT" && shasum -a 256 *.msi > SHA256SUMS 2>/dev/null; ls -l
