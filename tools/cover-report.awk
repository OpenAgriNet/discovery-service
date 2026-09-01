# Per-package rollup of a Go coverage profile.
#
# `go tool cover` reports per function (hundreds of lines) or per file (HTML).
# Neither answers "which package is thin", which is the question worth asking
# before writing a test. This aggregates the profile to one row per package.
#
# It lives in a file rather than inline in the Makefile because make eats `$`:
# every `$1` would have to be written `$$1`, and a mis-escaped field reads as
# the empty string instead of failing — an aggregation that silently reports
# zeros is worse than one that does not run.
#
# THE DEDUPLICATION IS THE WHOLE TRICK. Under -coverpkg every source block
# appears once per test binary that was built — the same statement shows up
# under `src/publish`'s run, the acceptance suite's run, and so on. Summing the
# statement counts therefore multiplies the denominator by the number of
# binaries, and treating a block as missed because *this* binary did not reach
# it discards the coverage another binary provided. So: key on the block, keep
# its statement count once, and mark it covered if ANY binary hit it. That is
# what `go tool cover` does internally, which is why the TOTAL printed here
# agrees with `go tool cover -func | tail -1`. If it ever disagrees, this file
# is wrong, not the profile.
#
# Usage: awk -f tools/cover-report.awk coverage.out

NR == 1 { next }  # "mode: atomic"

{
    statements[$1] = $2
    if ($3 > 0) {
        hit[$1] = 1
    }
}

END {
    if (length(statements) == 0) {
        print "no coverage blocks in the profile — did the test run write one?" > "/dev/stderr"
        exit 1
    }

    for (block in statements) {
        # A block key is "import/path/file.go:12.34,15.6". Strip the position,
        # then the file, to get the package's import path.
        split(block, field, ":")
        package = field[1]
        sub(/\/[^\/]+$/, "", package)
        sub(/^github\.com\/OpenAgriNet\/discovery-service\//, "", package)

        total[package] += statements[block]
        grandTotal += statements[block]
        if (block in hit) {
            covered[package] += statements[block]
            grandCovered += statements[block]
        }
    }

    # Sorted here rather than by piping to sort(1), which cannot tell a data row
    # from the header or the TOTAL and ranks all three by coverage — putting the
    # header last and the total somewhere in the middle of the packages.
    #
    # A selection sort over ~20 packages, because POSIX awk has no asort: that
    # is a gawk extension, and this has to run against the BWK awk that ships
    # with macOS.
    count = 0
    for (package in total) {
        name[++count] = package
    }
    for (i = 1; i < count; i++) {
        best = i
        for (j = i + 1; j <= count; j++) {
            if (percent(name[j]) > percent(name[best])) {
                best = j
            }
        }
        swap = name[i]; name[i] = name[best]; name[best] = swap
    }

    printf "%-40s %7s %8s\n", "PACKAGE", "STMTS", "COVER"
    for (i = 1; i <= count; i++) {
        printf "%-40s %7d %7.1f%%\n", name[i], total[name[i]], percent(name[i])
    }
    printf "%-40s %7s %8s\n", "", "", ""
    printf "%-40s %7d %7.1f%%\n", "TOTAL", grandTotal, 100 * grandCovered / grandTotal
}

function percent(package) {
    return 100 * covered[package] / total[package]
}
