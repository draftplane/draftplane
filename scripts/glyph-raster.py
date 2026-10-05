#!/usr/bin/env python3
"""Rasterise this view's vertical glyphs and print the figures quoteBarGlyph
records (ui/painted.go).

WHY IT IS COMMITTED. The numbers at quoteBarGlyph chose the quote bar over two
incumbents and one rejected candidate, and for a while they were second-hand:
corrected against a table somebody else had produced, with no date, no owner
and -- the part that actually cost -- no record of how the cell was defined
or where the ink threshold sat, so nobody could re-derive them and find out
whether they still held. This file is that record. It is a one-off
measurement tool, not part of the build: no make target runs it, `go test`
never sees it, and it can only tell you about the fonts on the machine it runs
on.

METHOD, which is the whole point of committing it:

  * each glyph is rendered at 96px through Pillow's FreeType binding, pen set
    at the LEFT BASELINE of a known origin (anchor="ls"), into an L canvas;
  * the CELL is the face's monospace advance (hhea.advanceWidthMax, which for
    a monospace face is every glyph's advance) by hhea.ascent - hhea.descent,
    both scaled from unitsPerEm to 96px;
  * INK is a pixel whose coverage is >= THRESH. Both 1 (any coverage) and 128
    (half) are printed, because one conclusion below depends on the choice and
    a buried threshold is how the previous figures became unreproducible;
  * WEIGHT is ink pixels over cell-box pixels; X-SPAN is the ink bounding box
    as a fraction of the cell advance, measured from the pen origin; IoU is
    over the ink pixel SETS with both glyphs at the same cell origin;
  * a face whose cmap lacks a codepoint is SKIPPED rather than rendered. PIL
    would draw .notdef, and .notdef is not the glyph being measured.

THE STYLED SECTION IS NOT DECORATION. A weight comparison between the quote
bar and the comment marker is only about the screen if both are measured in
the face they are DRAWN in, and they are not the same face: the bar is
st.Dim, which is neither bold nor italic, while every '|' on screen is bold --
CardHeader is Bold and CardHeaderResolved, the pair the bar was moved for, is
Bold+Italic (ui/styles.go). '▎' is a block element and does not gain ink when
the weight axis moves; '|' does. So `--styled` is where the on-screen answer
is, and the regular-face table above it is the geometry, not the pixel a
reader sees.

Run it:

    python3 -m venv /tmp/rasterenv && /tmp/rasterenv/bin/pip install pillow fonttools
    /tmp/rasterenv/bin/python scripts/glyph-raster.py            # the recorded figures
    /tmp/rasterenv/bin/python scripts/glyph-raster.py --styled    # '|' as it is drawn
    /tmp/rasterenv/bin/python scripts/glyph-raster.py --forensics # what moved the previously recorded figures

Faces are looked up by path and skipped if absent, so it degrades to whatever
is installed rather than failing.
"""

import os
import sys

try:
    from PIL import Image, ImageDraw, ImageFont
    from fontTools.ttLib import TTFont, TTCollection
except ImportError:
    sys.exit("needs Pillow and fontTools: pip install pillow fonttools")

PX = 96
PAD = 96
THRESHOLDS = (1, 128)
HOME = os.path.expanduser("~")

# The four monospace faces the recorded figures are measured in. SF Mono's
# DEFAULT INSTANCE is Light (the fvar default is wght 294.67, not 400), which
# is why it holds the thinnest '|' of the four -- worth knowing before reading
# a range whose low end comes from it.
FACES = [
    ("Menlo", "/System/Library/Fonts/Menlo.ttc", 0, None),
    ("SF Mono", "/System/Library/Fonts/SFNSMono.ttf", 0, None),
    ("Hack", HOME + "/Library/Fonts/Hack-Regular.ttf", 0, None),
    ("Ubuntu Mono", HOME + "/Library/Fonts/UbuntuMono-Regular.ttf", 0, None),
]

# The same four families in the states this view actually draws: Dim (neither
# bold nor italic) for the quote bar, Bold for CardHeader, Bold+Italic for
# CardHeaderResolved. SF Mono is one variable file per slant, so the instance
# is named rather than the file.
STYLED = {
    "Menlo": [
        ("Regular", "/System/Library/Fonts/Menlo.ttc", 0, None),
        ("Bold", "/System/Library/Fonts/Menlo.ttc", 1, None),
        ("Italic", "/System/Library/Fonts/Menlo.ttc", 2, None),
        ("Bold Italic", "/System/Library/Fonts/Menlo.ttc", 3, None),
    ],
    "SF Mono": [
        ("Light (file default)", "/System/Library/Fonts/SFNSMono.ttf", 0, None),
        ("Regular", "/System/Library/Fonts/SFNSMono.ttf", 0, "Regular"),
        ("Bold", "/System/Library/Fonts/SFNSMono.ttf", 0, "Bold"),
        ("Heavy", "/System/Library/Fonts/SFNSMono.ttf", 0, "Heavy"),
        ("Regular Italic", "/System/Library/Fonts/SFNSMonoItalic.ttf", 0, "Regular Italic"),
        ("Bold Italic", "/System/Library/Fonts/SFNSMonoItalic.ttf", 0, "Bold Italic"),
    ],
    "Hack": [
        ("Regular", HOME + "/Library/Fonts/Hack-Regular.ttf", 0, None),
        ("Bold", HOME + "/Library/Fonts/Hack-Bold.ttf", 0, None),
        ("Italic", HOME + "/Library/Fonts/Hack-Italic.ttf", 0, None),
        ("Bold Italic", HOME + "/Library/Fonts/Hack-BoldItalic.ttf", 0, None),
    ],
    "Ubuntu Mono": [
        ("Regular", HOME + "/Library/Fonts/UbuntuMono-Regular.ttf", 0, None),
        ("Bold", HOME + "/Library/Fonts/UbuntuMono-Bold.ttf", 0, None),
        ("Italic", HOME + "/Library/Fonts/UbuntuMono-Italic.ttf", 0, None),
        ("Bold Italic", HOME + "/Library/Fonts/UbuntuMono-BoldItalic.ttf", 0, None),
    ],
}

GLYPHS = [
    ("quarter", "▎", "LEFT ONE QUARTER BLOCK -- quoteBarGlyph"),
    ("eighth", "▏", "LEFT ONE EIGHTH BLOCK -- quoteBarGlyph's earlier value"),
    ("light", "│", "BOX DRAWINGS LIGHT VERTICAL -- the grid's box, and the candidate ruled out"),
    ("heavy", "┃", "BOX DRAWINGS HEAVY VERTICAL -- cursorGlyph"),
    ("pipe", "|", "VERTICAL LINE -- commentMarker"),
]
CH = {g: c for g, c, _ in GLYPHS}

# Every pair the comment makes a claim about, including heavy-vs-pipe: the
# comment calls one pair the highest-scoring of this view's verticals, and a
# superlative over a set that is not all measured is not a measurement.
PAIRS = [("quarter", "pipe"), ("quarter", "heavy"), ("quarter", "light"),
         ("eighth", "pipe"), ("eighth", "heavy"), ("eighth", "light"),
         ("light", "pipe"), ("heavy", "pipe"), ("heavy", "light")]

# What was previously recorded, as ranges over the three faces that carry
# the block glyphs. --forensics asks which of them this machine can still
# produce.
RECORDED = {"heavy": (19.1, 25.9), "pipe": (7.0, 11.8), "eighth": (9.6, 11.2)}


def metrics(path, index):
    if path.endswith(".ttc"):
        tt = TTCollection(path).fonts[index]
    else:
        tt = TTFont(path, fontNumber=index or 0)
    head, hhea = tt["head"], tt["hhea"]
    return head.unitsPerEm, hhea.advanceWidthMax, hhea.ascent, hhea.descent, set(tt.getBestCmap())


def cell_of(path, index):
    upem, adv, asc, desc, cmap = metrics(path, index)
    s = PX / upem
    return adv * s, (asc - desc) * s, asc * s, cmap


def ink(path, index, variation, ch, cell_w, cell_h, asc_px, thresh):
    font = ImageFont.truetype(path, PX, index=index)
    if variation:
        font.set_variation_by_name(variation)
    img = Image.new("L", (int(cell_w) + 2 * PAD, int(cell_h) + 2 * PAD), 0)
    ImageDraw.Draw(img).text((PAD, PAD + asc_px), ch, font=font, fill=255, anchor="ls")
    px = img.load()
    w, h = img.size
    return {(x, y) for y in range(h) for x in range(w) if px[x, y] >= thresh}


def weight(pts, area):
    return 100.0 * len(pts) / area


def recorded_figures():
    for name, path, index, variation in FACES:
        if not os.path.exists(path):
            print("%s: not installed (%s)" % (name, path))
            continue
        cell_w, cell_h, asc_px, cmap = cell_of(path, index)
        area = cell_w * cell_h
        print("\n=== %s === cell %.1f x %.1f px = %.0f px^2" % (name, cell_w, cell_h, area))
        missing = [g for g, ch, _ in GLYPHS if ord(ch) not in cmap]
        print("    not in cmap: %s" % (", ".join(missing) if missing else "none"))
        masks = {}
        for thresh in THRESHOLDS:
            for g, ch, what in GLYPHS:
                if ord(ch) not in cmap:
                    continue
                pts = ink(path, index, variation, ch, cell_w, cell_h, asc_px, thresh)
                masks[(g, thresh)] = pts
                xs = [p[0] for p in pts]
                print("    thr%-4d %-8s %s  weight %5.2f%%  x[%+.3f,%+.3f]  centroid %+.3f   %s"
                      % (thresh, g, ch, weight(pts, area),
                         (min(xs) - PAD) / cell_w, (max(xs) + 1 - PAD) / cell_w,
                         (sum(xs) / len(xs) + 0.5 - PAD) / cell_w, what))
        for thresh in THRESHOLDS:
            for a, b in PAIRS:
                A, B = masks.get((a, thresh)), masks.get((b, thresh))
                if not A or not B:
                    continue
                gap = abs(sum(p[0] for p in A) / len(A) - sum(p[0] for p in B) / len(B)) / cell_w
                print("    thr%-4d IoU %-8s vs %-8s %.3f   centroid gap %.3f cell widths"
                      % (thresh, a, b, len(A & B) / len(A | B), gap))


def styled():
    """'|' in the states it is DRAWN in, against the bar in the state IT is
    drawn in. The bar is never bold; the marker always is."""
    print("\n'|' AS DRAWN. st.Dim (the bar) is neither bold nor italic; CardHeader is Bold and")
    print("CardHeaderResolved is Bold+Italic, so every '|' on screen carries SGR 1. Weight at")
    print("threshold 128, as a %% of that instance's own cell.\n")
    for family, instances in STYLED.items():
        print("=== %s ===" % family)
        bar = None
        rows = []
        for label, path, index, variation in instances:
            if not os.path.exists(path):
                print("    %-22s not installed" % label)
                continue
            cell_w, cell_h, asc_px, cmap = cell_of(path, index)
            area = cell_w * cell_h
            out = []
            for g in ("quarter", "pipe"):
                if ord(CH[g]) not in cmap:
                    out.append((g, None, None))
                    continue
                pts = ink(path, index, variation, CH[g], cell_w, cell_h, asc_px, 128)
                out.append((g, weight(pts, area), pts))
            rows.append((label, dict((g, w) for g, w, _ in out), dict((g, p) for g, _, p in out)))
            if bar is None and out[0][1] is not None:
                bar = out[0][1]
        for label, w, masks in rows:
            q, p = w.get("quarter"), w.get("pipe")
            ratio = "" if not (bar and p) else "   bar:marker %.2fx" % (bar / p)
            print("    %-22s '▎' %s   '|' %s%s"
                  % (label,
                     "not in cmap" if q is None else "%5.2f%%" % q,
                     "not in cmap" if p is None else "%5.2f%%" % p,
                     ratio))
        # Position, which is the axis that does not move under styling.
        reg = next((r for r in rows if r[0].startswith(("Regular", "Light"))), None)
        for label, w, masks in rows:
            if reg is None or masks.get("pipe") is None or reg[2].get("quarter") is None:
                continue
            A, B = reg[2]["quarter"], masks["pipe"]
            print("    IoU '▎' (as drawn, regular) vs '|' in %-14s %.3f" % (label, len(A & B) / len(A | B)))


def forensics():
    """Which of the previously recorded ranges can this machine still produce?

    A CELL REDEFINITION SCALES EVERY GLYPH IN A FACE BY ONE FACTOR, so it can
    move a weight but cannot move a within-face RATIO. That is what makes the
    old '┃' figure answerable even though the old run's method is unrecorded:
    for each face, solve for the scale s that would put '┃' at the recorded
    low end, then ask whether '|' and '▏' scaled by the same s still land
    inside their own recorded ranges. If none does, no cell definition
    produces the recorded row and the difference is not a cell difference.
    """
    lo_heavy = RECORDED["heavy"][0]
    for thresh in (1, 64, 128, 192, 254):
        print("\n--- ink threshold %d ---" % thresh)
        for name, path, index, variation in FACES:
            if not os.path.exists(path):
                continue
            cell_w, cell_h, asc_px, cmap = cell_of(path, index)
            area = cell_w * cell_h
            w = {}
            for g in ("heavy", "pipe", "eighth"):
                if ord(CH[g]) not in cmap:
                    continue
                w[g] = weight(ink(path, index, variation, CH[g], cell_w, cell_h, asc_px, thresh), area)
            if "heavy" not in w:
                print("  %-12s '┃' not in cmap -- cannot be the source of %.1f%%" % (name, lo_heavy))
                continue
            s = lo_heavy / w["heavy"]
            checks = []
            for g in ("pipe", "eighth"):
                lo, hi = RECORDED[g]
                v = w[g] * s
                checks.append("'%s' -> %5.2f%% %s [%.1f,%.1f]" % (CH[g], v, "IN " if lo <= v <= hi else "OUT", lo, hi))
            print("  %-12s '┃' %5.2f%%  '|' %5.2f%%  ratio %.3f | to reach %.1f%% needs cell x%.3f: %s"
                  % (name, w["heavy"], w["pipe"], w["heavy"] / w["pipe"], lo_heavy, s, "; ".join(checks)))


def main():
    if "--styled" in sys.argv:
        styled()
    elif "--forensics" in sys.argv:
        forensics()
    else:
        recorded_figures()


main()
