/**
 * The colour theme, resolved once and before first paint (#1236).
 *
 * index.html runs a small inline script that puts `data-theme` on the root
 * element ahead of any module, because the boot is async (#1216) and an
 * effect in the app runs after the first paint. This is the app's side of
 * the same rule: start from what the page already says, then the stored
 * preference, then light. `prefers-color-scheme` is not consulted, as before:
 * a light default is the product's choice until an operator picks dark.
 */
export type Theme = "light" | "dark";

export const THEME_KEY = "retnd.theme";

function isTheme(v: string | null): v is Theme {
  return v === "light" || v === "dark";
}

function stored(): Theme {
  try {
    return window.localStorage.getItem(THEME_KEY) === "dark" ? "dark" : "light";
  } catch {
    return "light";
  }
}

/** The theme the page is in: the root's own attribute when it is valid, else
 *  the stored preference, else light. */
export function resolveTheme(): Theme {
  const set = document.documentElement.getAttribute("data-theme");
  return isTheme(set) ? set : stored();
}

/** Writes the theme to the root element and remembers it. */
export function applyTheme(theme: Theme) {
  document.documentElement.setAttribute("data-theme", theme);
  try {
    window.localStorage.setItem(THEME_KEY, theme);
  } catch {
    // Storage is blocked: the theme still applies for this page.
  }
}
