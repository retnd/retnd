/**
 * The in-app mark is drawn in JSX (so --accent can colour it) and the
 * product's one logo, docs/site/assets/logo.svg, draws the same ring as its `#mark`
 * group. Two drawings of one mark is how the repository ended up with two
 * blues for it before, so this reads the real file and holds the component
 * to it.
 *
 * It asserts at size 24, the size the app header draws it at. Below 20px
 * Logo thickens the ring and the dot on purpose (a 5-unit stroke is under a
 * pixel and a half at 16px), which is a deliberate departure from the file
 * and not drift.
 */
import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Logo } from "@shared/components/Logo";
// Through Vite's `?raw` rather than node:fs, for the reason
// dark-mode-contrast.test.ts gives: tsconfig.json deliberately keeps
// @types/node out of this program, and `?raw` is typed by vite/client.
import source from "../../../../docs/site/assets/logo.svg?raw";

/** The `#mark` group of the logo file, parsed rather than pattern-matched. */
function markCircles(): Element[] {
  const doc = new DOMParser().parseFromString(source, "image/svg+xml");
  const mark = doc.getElementById("mark");
  if (!mark) throw new Error("docs/site/assets/logo.svg has no #mark group");
  return [...mark.querySelectorAll("circle")];
}

describe("the in-app mark and the product's one logo", () => {
  it("draw the same ring and dot", () => {
    const [fileRing, fileDot] = markCircles();
    const { container } = render(<Logo size={24} />);
    const [ring, dot] = [...container.querySelectorAll("circle")];

    for (const attr of ["cx", "cy", "r", "stroke-width", "stroke-linecap", "stroke-dasharray", "transform"]) {
      expect(ring.getAttribute(attr), `ring ${attr}`).toBe(fileRing.getAttribute(attr));
    }
    for (const attr of ["cx", "cy", "r"]) {
      expect(dot.getAttribute(attr), `dot ${attr}`).toBe(fileDot.getAttribute(attr));
    }
  });
});
