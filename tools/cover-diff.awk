# Coverage of a specific set of files, from a Go coverage profile.
#
# Reads two inputs: the changed-file list on stdin (one repo-relative path per
# line), then the profile. Prints
#
#   FILE\t<pct>\t<path>   once per file below `min`, and
#   TOTAL\t<pct>          the aggregate over every file in the list, or
#   EMPTY                 when none of them carry a coverable statement.
#
# -v mod=<module path with trailing slash>  to turn a repo-relative path into
#                                           the profile's fully-qualified one
# -v min=<percent>                          the threshold FILE rows report against
#
# It lives in a file rather than inline in the Makefile for the same reason
# cover-report.awk does: make eats `$`, so every `$1` would be written `$$1`,
# and a mis-escaped field reads as the empty string instead of failing. It is
# also the only way the thing can be tested, which is what
# tests/covertool/cover_diff_test.go now does.
#
# THE DEDUPLICATION IS THE WHOLE TRICK — the same one cover-report.awk turns
# on, and for the same reason. Under -coverpkg every source block appears once
# per test binary that was built: measured on this repo, 2294 blocks × 21
# copies and 156 × 22. Summing per profile LINE therefore multiplies the
# denominator by the number of binaries while crediting the numerator only for
# the binaries that happened to reach the block, so a well-tested file reports
# a single-digit percentage. This is not hypothetical: before the fix,
# src/publish/controller.go read 7% against a real 81%, and the gate failed
# every PR that touched a .go file.
#
# So: key on the BLOCK, keep its statement count once, and count it covered if
# ANY binary hit it. The TOTAL this prints then agrees with
# `go tool cover -func`, which is the invariant the test pins.
#
# Usage: printf '%s\n' src/foo.go | awk -v mod=example.com/m/ -v min=80 \
#            -f tools/cover-diff.awk - coverage.out

NR == FNR { want[mod $0] = 1; next }

# "mode: atomic" falls out here too: $1 is "mode:", which is not a path in the
# changed set, so it is skipped like any other unwanted file.
{
    f = $1
    sub(/:.*/, "", f)
    if (!(f in want)) next

    statements[$1] = $(NF - 1)
    owner[$1] = f
    if ($NF > 0) hit[$1] = 1
}

END {
    for (block in statements) {
        f = owner[block]
        tot[f] += statements[block]
        if (block in hit) cov[f] += statements[block]
    }

    T = 0
    C = 0
    for (f in tot) {
        # A file whose blocks are all zero-statement contributes nothing and
        # has no percentage. Dividing anyway aborts awk with "division by
        # zero" BEFORE the T == 0 check below can report EMPTY, which turned
        # an unmeasurable file into a crashed gate rather than a clean skip.
        if (tot[f] == 0) continue

        T += tot[f]
        C += cov[f]
        p = int(cov[f] * 100 / tot[f])
        disp = f
        sub("^" mod, "", disp)
        if (p < min) print "FILE\t" p "\t" disp
    }

    if (T == 0) {
        print "EMPTY"
        exit
    }
    print "TOTAL\t" int(C * 100 / T)
}
