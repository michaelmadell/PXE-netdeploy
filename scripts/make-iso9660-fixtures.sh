#!/bin/sh
# Makes the ISO9660 reader's test images (internal/media/iso9660/testdata)
# with xorriso (Debian/Ubuntu: apt install xorriso; on Windows, run it in
# WSL). Same tree in each image; only the name extensions differ. The
# images are gzipped: they are mostly padding.
set -eu
out=$(cd "$(dirname "$0")/../internal/media/iso9660/testdata" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
t="$tmp/tree"
mkdir -p "$t/EFI/BOOT" "$t/Deep/a/b/c/d" "$t/sources/sxs" "$t/.disk" "$t/casper"
printf 'not really a boot manager' > "$t/EFI/BOOT/BOOTX64.EFI"
: > "$t/empty.txt"
printf 'leaf' > "$t/Deep/a/b/c/d/leaf.txt"
printf 'unicode name' > "$t/sources/Ünïcode-文件.txt"
printf 'Ubuntu-Server 26.04.1 LTS "Resolute Raccoon" - Release amd64 (20260826)' > "$t/.disk/info"
printf 'kernel' > "$t/casper/vmlinuz"
printf 'initrd' > "$t/casper/initrd"
# 200 KiB + 5 bytes, byte i = (i*7) mod 256.
python3 -c 'import sys; sys.stdout.buffer.write(bytes((i*7)&255 for i in range(200*1024+5)))' > "$t/sources/big.bin"
i=0
while [ $i -lt 150 ]; do
  printf '%d' $i > "$t/sources/sxs/file-$(printf '%03d' $i).cab"
  i=$((i+1))
done
# A name long enough that Rock Ridge needs a continuation area (CE).
long=$(printf 'long-name-%.0s' 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20)
printf 'long' > "$t/$long.txt"
ln -s ../empty.txt "$t/sources/link-to-empty"

xorriso -as mkisofs -quiet -V NDTEST_RRJOLIET -R -J -joliet-long -o "$tmp/rr-joliet.iso" "$t"
# xorriso always adds Rock Ridge in mkisofs mode; native commands turn it off.
xorriso -report_about SORRY -outdev "$tmp/joliet.iso" -volid NDTEST_JOLIET -rockridge off -joliet on \
  -compliance joliet_long_names -map "$t" /
xorriso -report_about SORRY -outdev "$tmp/plain.iso" -volid NDTEST_PLAIN -rockridge off -joliet off -map "$t" /
for n in rr-joliet joliet plain; do
  gzip -9n < "$tmp/$n.iso" > "$out/$n.iso.gz"
done
ls -l "$out"
