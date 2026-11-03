#!/usr/bin/env bash
# Fails when any package is under the minimum statement or function coverage.
set -euo pipefail

profile="${1:-coverage.out}"
minimum="${2:-90}"

{
    awk 'NR > 1 {
        package = $1
        sub(/\/[^\/]*$/, "", package)
        print "statement", package, $2, ($3 > 0 ? $2 : 0)
    }' "$profile"
    go tool cover -func="$profile" | awk '$1 != "total:" {
        package = $1
        sub(/\/[^\/]*$/, "", package)
        print "function", package, 1, ($NF + 0 > 0 ? 1 : 0)
    }'
} | awk -v minimum="$minimum" '
    { key = $2 " " $1; total[key] += $3; covered[key] += $4 }
    END {
        for (key in total) {
            percent = 100 * covered[key] / total[key]
            printf "%s: %.2f%%\n", key, percent
            if (percent < minimum) failed = 1
        }
        if (length(total) == 0) {
            print "No coverage data found"
            failed = 1
        }
        exit failed
    }' | sort
