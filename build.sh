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
res() { # dir manifest product description exe-name
  $WINRES simply --arch amd64 --manifest "$2" --out "$1/rsrc" --icon assets/icon.png \
    --product-version "$VERSION.0" --file-version "$VERSION.0" \
    --product-name "$3" --file-description "$4" --original-filename "$5"
}
res cmd/slipdrukka-server cli "SlipDrukka Server" "SlipDrukka Server service" slipdrukka-server.exe
res cmd/slipdrukka-agent  cli "SlipDrukka Agent"  "SlipDrukka — shop printing service" SlipDrukka-service.exe
res cmd/slipdrukka-tray   gui "SlipDrukka"        "SlipDrukka — status icon and shop page" SlipDrukka.exe
export GOOS=windows GOARCH=amd64 CGO_ENABLED=0
LD="-s -w -X main.version=$VERSION"
go build -trimpath -ldflags "$LD" -o "$OUT/slipdrukka-server.exe" ./cmd/slipdrukka-server
go build -trimpath -ldflags "$LD" -o "$OUT/SlipDrukka-service.exe" ./cmd/slipdrukka-agent
go build -trimpath -ldflags "$LD -H=windowsgui" -o "$OUT/SlipDrukka.exe" ./cmd/slipdrukka-tray
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
  # wixl has no CustomAction/@Directory: close the old status icon before files are checked
  # (type 34 = exe in a directory, +64 = ignore its exit code). See installer/agent.wxs.
  msibuild "$OUT/slipdrukka-agent-$VERSION.msi" -q "INSERT INTO \`CustomAction\` (\`Action\`,\`Type\`,\`Source\`,\`Target\`) VALUES ('SDStopTray',98,'SystemFolder','\"[SystemFolder]taskkill.exe\" /F /IM SlipDrukka.exe /IM slipdrukka-tray.exe')"
  # wixl has no Shortcut/@Show: open the panel shortcut minimized (7), so its console
  # window does not flash. See installer/server.wxs.
  msibuild "$OUT/slipdrukka-server-$VERSION.msi" -q "UPDATE \`Shortcut\` SET \`ShowCmd\`=7"
else
  echo "wixl not found — MSI packages skipped (brew install msitools)" >&2
fi
# Checksums of what is published: the two installers.
cd "$OUT" && shasum -a 256 *.msi > SHA256SUMS 2>/dev/null; ls -l
