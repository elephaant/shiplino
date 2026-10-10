#!/bin/sh
# Tests the npm packaging end to end without the network: fake release
# archives -> build.mjs -> npm pack -> global install into a temp prefix ->
# run the launcher. Also checks that a tampered archive stops the build.
# Linux x64 only (the fake binary is a shell script). Needs node, npm,
# tar and zip.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
v=1.2.3-test.1

mkdir -p "$work/assets" "$work/stage"
cd "$work/stage"
printf '#!/bin/sh\necho "fake shiplino $*"\nexit 3\n' > shiplino
chmod 755 shiplino
cp shiplino shiplino.exe
for t in darwin_arm64 darwin_amd64 linux_arm64 linux_amd64; do
	tar -czf "$work/assets/shiplino_${v}_$t.tar.gz" shiplino
done
for t in windows_arm64 windows_amd64; do
	zip -q "$work/assets/shiplino_${v}_$t.zip" shiplino.exe
done
cd "$work/assets"
sha256sum ./*.tar.gz ./*.zip | sed 's| \./| |' > checksums.txt

node "$here/build.mjs" --version "v$v" --assets "$work/assets" --out "$work/out"

mkdir "$work/tgz"
cd "$work/tgz"
npm pack --silent "$work/out/shiplino" "$work/out/@shiplino/linux-x64" > /dev/null
npm install -g --silent --no-audit --no-fund --prefix "$work/prefix" \
	"$work/tgz/shiplino-linux-x64-$v.tgz" "$work/tgz/shiplino-$v.tgz"

status=0
out=$("$work/prefix/bin/shiplino" version --x) || status=$?
if [ "$out" != "fake shiplino version --x" ] || [ "$status" != 3 ]; then
	echo "launcher: got output '$out' and exit $status, want 'fake shiplino version --x' and 3" >&2
	exit 1
fi

printf 'x' >> "$work/assets/shiplino_${v}_linux_arm64.tar.gz"
if node "$here/build.mjs" --version "$v" --assets "$work/assets" --out "$work/out2" > /dev/null 2>&1; then
	echo "build.mjs accepted an archive that doesn't match checksums.txt" >&2
	exit 1
fi
echo "npm packaging OK"
