#!/usr/bin/env bash
# Runs one slice of the tests of a single Go package, so a package too slow for
# one CI job can be spread over a matrix without leaving any test out.
#
#   scripts/test-shard.sh <shard 1..count> <count> <package> [go test flags...]
#
# The tests are listed with go test -list, sorted by name, and test number i
# belongs to shard ((i - 1) mod count) + 1. Every test lands in exactly one
# shard; the script fails when a shard would be empty or the listing is empty.
set -euo pipefail

if [ "$#" -lt 3 ]; then
  echo "usage: $0 <shard> <count> <package> [go test flags...]" >&2
  exit 2
fi
shard=$1
count=$2
pkg=$3
shift 3

case "$shard$count" in
  *[!0-9]*) echo "shard and count must be numbers" >&2; exit 2 ;;
esac
if [ "$shard" -lt 1 ] || [ "$shard" -gt "$count" ]; then
  echo "shard $shard is outside 1..$count" >&2
  exit 2
fi

names=$(go test -list '.' "$pkg" | grep -E '^(Test|Example|Fuzz)' | LC_ALL=C sort)
total=$(printf '%s\n' "$names" | grep -c .)
if [ "$total" -eq 0 ]; then
  echo "no tests listed for $pkg" >&2
  exit 1
fi
mine=$(printf '%s\n' "$names" | awk -v i="$shard" -v n="$count" '(NR - 1) % n == i - 1' | paste -sd'|' -)
if [ -z "$mine" ]; then
  echo "shard $shard of $count has no tests ($total in $pkg)" >&2
  exit 1
fi
echo "shard $shard of $count: $(printf '%s\n' "$mine" | tr '|' '\n' | grep -c .) of $total tests in $pkg"
go test "$@" -run "^($mine)\$" "$pkg"
