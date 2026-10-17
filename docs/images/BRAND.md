# Bifrost logo

`bifrost-logo.png` is the finished horizontal logo for the README: a violet/cyan
angular bridge emblem with an off-white wordmark on charcoal. Preserve the
aspect ratio and clear space. The full-resolution asset is 2172 × 724 pixels.

Created on 17 October 2026 using the built-in imagegen tool, followed by a cleanup
edit. This file is a logo asset; screenshots elsewhere in this directory are
captures of the actual native application.

Final cleanup prompt:

> Refine this Bifrost logo into a completely clean final brand asset. Preserve the
> violet/cyan angular bridge emblem, exact wordmark 'Bifrost', and horizontal
> layout. Remove white splatter, speckle, grunge, jagged outline artifacts and
> distressed pixels around and within the letters and emblem. Use solid clean
> off-white geometric sans serif letters and flat violet/cyan emblem shapes with
> clean edges. Render on an entirely solid opaque charcoal background #111218.
> No transparency, texture, shadow, glow, gradient or distress. Keep generous
> balanced margins and a wide 3:1 canvas.

Initial direction: an original minimal bridge/portal emblem for a native Go AI
developer workspace, with two structural uprights and an angular span, violet
#b39cff and cyan #76dce4 accents, and the exact wordmark “Bifrost”.


The terminal rendition lives in `internal/ui/brand.go`: ordinary Unicode cells in
violet/cyan form the same portal/bridge, with a compact `|>|` ASCII alternative.
`internal/ui/testdata/snapshots/tui-brand-160x48.ansi` is actual rendered TUI output;
its README PNG companion uses DejaVu Sans Mono. No terminal bitmap protocol is used.
