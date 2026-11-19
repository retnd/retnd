#!/usr/bin/env bash
# Re-export every raster brand asset from the one committed logo, docs/site/assets/logo.svg
# (FR-44, #893).
#
# WHY THIS IS A SCRIPT AND NOT A README PARAGRAPH. FR-44's requirement is
# not "the favicons were regenerated once"; it is that every declared size
# derives from one source, so that the next person who touches the mark
# cannot re-export five of the eight and leave three showing the old
# drawing. There used to be four SVG sources and they were four separate
# drawings of one mark; there is one now, so there is nothing left to
# re-export from a different place, and a change to docs/site/assets/logo.svg reaches
# every size below by running this file.
#
# THE MARK ALONE IS CUT OUT OF THE LOGO, NOT TAKEN FROM A SECOND FILE. The
# mark occupies the left 48 units of the 122.2-unit lockup, so the lockup is
# rendered at the pixel height wanted and the left square is cropped off. A
# user stylesheet (librsvg's --stylesheet) does the two things a consumer of
# the split logo does: it sets the mark's colour, and it hides the wordmark
# group. librsvg does not evaluate prefers-color-scheme, so the colour is
# always the one named here, never whichever scheme the machine is in.
#
# The framings below are those measurements, written down:
#
#   natural      the 48-unit viewBox rendered straight, so the mark's ink
#                occupies 39/48 = 81.25% of the square. The two favicon
#                PNGs and both .ico members. A favicon is already tiny and
#                padding it wastes the only pixels it has.
#   safe-area    the mark at 49% of the canvas, centred. Apple's touch
#                icon is drawn on a full-bleed tile that the OS rounds and
#                may add effects to, so the art keeps well clear of the
#                edge; docs/assets/logo-mark-256.png was made at the same
#                ratio and keeps it.
#   bleed        the mark's ink filling the canvas height, centred, on a
#                2:1 canvas. docs/assets/logo-mark-640x320.png only.
#
# 49% of the canvas is an ink box of 0.49*C, and the ink is 39/48 of the
# render size, so the render size is 0.49*48/39 = 0.6031 of the canvas.
#
# Needs rsvg-convert (librsvg) and ImageMagick's `magick`. Both are in the
# image-tooling set this repository already assumes for docs/site/tools;
# the script names whichever is missing and exits 1 rather than producing a
# half-regenerated set.
#
# Idempotent by construction: same source, same sizes, same output. Run it
# and `git status` should be clean unless the source actually changed.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

missing=""
for tool in rsvg-convert magick; do
  command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
if [ -n "$missing" ]; then
  echo "export-rasters: missing:$missing" >&2
  echo "export-rasters: install librsvg (rsvg-convert) and ImageMagick (magick), then re-run." >&2
  exit 1
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

logo="docs/site/assets/logo.svg"
[ -f "$logo" ] || { echo "export-rasters: $logo does not exist" >&2; exit 1; }

# The rasters carry the brand blue, #3a628f, as they always have. The
# logo's own default mark colour is the lighter #446b9c that the README
# lockup used; neither a favicon nor a touch-icon tile is that file's
# default render, so the colour is stated here rather than inherited.
brand_blue="#3a628f"

# mark <px> <colour> <out.png>
# The mark alone, on a transparent square px by px, in the given colour.
# Rendering the lockup at height px makes the mark's own 48-unit grid exactly
# px pixels wide, so cropping the left px columns is the mark's whole viewBox.
mark() {
  px="$1"; color="$2"; out="$3"
  printf '.retnd-mark-theme{color:%s!important}.retnd-wordmark-theme{display:none!important}\n' "$color" > "$tmp/mark.css"
  rsvg-convert -h "$px" -s "$tmp/mark.css" "$logo" -o "$tmp/wide.png"
  # -strip: ImageMagick stamps a creation and modification time into the PNG,
  # which made every run produce different bytes for the same picture.
  magick "$tmp/wide.png" -crop "${px}x${px}+0+0" +repage -strip "$out"
}

# natural <px> <colour> <out.png>
natural() {
  mark "$1" "$2" "$3"
}

# safe_area <canvas px> <colour> <out.png> [background]
# The mark at 49% of the canvas, centred. A background argument makes the
# tile opaque (Apple's icon is composited on white if it is not).
safe_area() {
  canvas="$1"; color="$2"; out="$3"; bg="${4:-none}"
  render="$(awk -v c="$canvas" 'BEGIN{printf "%d", (c*0.49*48/39)+0.5}')"
  mark "$render" "$color" "$tmp/sa.png"
  if [ "$bg" = none ]; then
    magick "$tmp/sa.png" -background none -gravity center -extent "${canvas}x${canvas}" "$out"
  else
    magick "$tmp/sa.png" -background "$bg" -gravity center -extent "${canvas}x${canvas}" \
      -alpha remove -alpha off "$out"
  fi
}

# bleed <w> <h> <colour> <out.png>
# The mark's ink filling the canvas height, centred on a wider canvas.
bleed() {
  cw="$1"; ch="$2"; color="$3"; out="$4"
  render="$(awk -v h="$ch" 'BEGIN{printf "%d", (h*48/39)+0.5}')"
  mark "$render" "$color" "$tmp/bl.png"
  magick "$tmp/bl.png" -background none -gravity center -extent "${cw}x${ch}" "$out"
}

# The two favicon sets are byte-identical today and stay that way: the site
# and the app ship the same mark at the same sizes, and two copies that
# drift is a bug nobody sees until one tab shows the old drawing.
for dir in docs/site/assets ui/shared/public; do
  natural 16 "$brand_blue" "$dir/favicon-16.png"
  natural 32 "$brand_blue" "$dir/favicon-32.png"

  # The .ico carries three members, 16, 32 and 48, which is what the
  # committed file already held; only the first two are also shipped as
  # standalone PNGs, so the 48 is rendered here and thrown away.
  natural 48 "$brand_blue" "$tmp/favicon-48.png"
  magick "$dir/favicon-16.png" "$dir/favicon-32.png" "$tmp/favicon-48.png" "$dir/favicon.ico"

  # The touch icon is the white mark on the brand tile: iOS ignores alpha
  # and composites on white, so an alpha PNG there is a white mark on
  # white.
  safe_area 180 "#ffffff" "$dir/apple-touch-icon.png" "$brand_blue"
done

safe_area 256 "$brand_blue" docs/assets/logo-mark-256.png
bleed 640 320 "$brand_blue" docs/assets/logo-mark-640x320.png

echo "export-rasters: ok (8 PNG, 2 ICO with three members each, from 1 SVG source)"
