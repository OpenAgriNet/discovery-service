"""Every intra-folder `](file.md#anchor)` link must resolve to a real heading.

Headings are positional in GitHub: the anchor is derived from the heading text,
so renaming a heading silently breaks every link to it. That is not a
hypothetical — renaming the entities broke four anchors in this folder, and a
broken anchor scrolls to the top of the page rather than erroring.

GitHub's slug: lowercase, drop anything not alphanumeric / `-` / `_` / space,
then space -> `-`. Consecutive hyphens are NOT collapsed, so `A — b` is
`a--b`; a checker that collapses them reports false breakage on every em dash.

archive/ is excluded: it is another team's design set, kept diffable against
its source, and its links point into its own tree.

Run:  python3 verify/links.py          (from docs/registry)
Needs: nothing
"""
import glob, io, os, re, sys


def slug(heading):
    h = re.sub(r"`|\*\*|\*|<[^>]+>", "", heading).strip()
    s = "".join(c for c in h.lower() if c.isalnum() or c in "-_ ")
    return s.replace(" ", "-")


def prose(md):
    """The file with fenced blocks dropped — a ``` block can hold an illustrative
    link that is not a link, and this README's own run list does."""
    keep, incode = [], False
    for line in md.split("\n"):
        if line.startswith("```"):
            incode = not incode
            continue
        keep.append("" if incode else line)
    return keep


def headings(lines):
    """Every anchor the file defines, with GitHub's -1/-2 suffix for repeats."""
    seen, out = {}, set()
    for line in lines:
        m = re.match(r"#{1,6}\s+(.*)$", line)
        if not m:
            continue
        a = slug(m.group(1))
        n = seen.get(a, 0)
        seen[a] = n + 1
        out.add(a if n == 0 else f"{a}-{n}")
    return out


def main():
    files = sorted(glob.glob("*.md") + glob.glob("verify/*.md"))
    md = {f: prose(io.open(f, encoding="utf-8").read()) for f in files}
    anchors = {f: headings(t) for f, t in md.items()}

    bad = unchecked = links = 0
    for f, lines in md.items():
        base = os.path.dirname(f)
        for target, frag in re.findall(r"\]\(([^)#\s]*)#([^)\s]+)\)", "\n".join(lines)):
            links += 1
            tf = os.path.normpath(os.path.join(base, target)) if target else f
            if tf not in anchors:
                print(f"  {f}: -> {target}#{frag}  (target outside checked set)")
                unchecked += 1
            elif frag not in anchors[tf]:
                print(f"  BROKEN  {f}: #{frag} -> {tf}")
                bad += 1

    print(f"\n{len(md)} files, {links} anchored links, "
          f"{sum(len(a) for a in anchors.values())} headings")
    print(f"broken: {bad}   unchecked targets: {unchecked}")
    return 1 if bad or unchecked else 0


if __name__ == "__main__":
    sys.exit(main())
