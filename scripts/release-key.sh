#!/bin/sh
# Puts the public half of the release signing key into the repository:
#
#   scripts/release-key.sh <ghosttree-release.pub> [repo root]
#
# The file is the one `minisign -G -p ghosttree-release.pub -s ...key` (with a password) writes.
# The key line lands in deploy/downloads/ghosttree/minisign.pub and in the
# pubkey='...' line of install.sh, which checks every release against it, and in
# internal/releasekey/minisign.pub, the copy ctx server update embeds. The
# secret half never comes near this script: it belongs in Bitwarden and in the
# secret MINISIGN_SECRET_KEY of the GitHub environment release, its password in
# MINISIGN_PASSWORD (RELEASING.md). Also used to rotate the key.
set -eu

[ "$#" -ge 1 ] || { echo "usage: $0 <public key file> [repo root]" >&2; exit 2; }
pub=$1
root=${2:-$(cd "$(dirname "$0")/.." && pwd)}
dir=$root/deploy/downloads/ghosttree

[ -f "$pub" ] || { echo "no such file: $pub" >&2; exit 1; }
[ "$(wc -l <"$pub" | tr -d ' ')" -eq 2 ] || { echo "$pub is not a minisign public key file (two lines expected)" >&2; exit 1; }
head -n 1 "$pub" | grep -q '^untrusted comment:' || { echo "$pub: the first line is not an 'untrusted comment:'" >&2; exit 1; }
key=$(sed -n 2p "$pub" | tr -d '\r')
# 42 bytes ("Ed", key id, key) are 56 base64 characters and start with "RW".
printf '%s\n' "$key" | grep -Eq '^RW[A-Za-z0-9+/]{54}$' || { echo "$pub: the second line is not an Ed25519 minisign public key" >&2; exit 1; }
[ -f "$dir/install.sh" ] || { echo "no installer at $dir/install.sh" >&2; exit 1; }

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
sed "s|^  pubkey='[^']*'|  pubkey='$key'|" "$dir/install.sh" >"$tmp"
grep -Fq "  pubkey='$key'" "$tmp" || { echo "install.sh has no pubkey='...' line to replace" >&2; exit 1; }
cat "$tmp" >"$dir/install.sh"
printf '%s\n%s\n' "$(sed -n 1p "$pub" | tr -d '\r')" "$key" >"$dir/minisign.pub"
embedded=$root/internal/releasekey
mkdir -p "$embedded"
cp "$dir/minisign.pub" "$embedded/minisign.pub"
echo "release key $key written to $dir/install.sh, $dir/minisign.pub and $embedded/minisign.pub"
