/**
 * The built-in structure check's rules as the browser states them (issue
 * #1111).
 *
 * Every sentence here is the one the server gives, word for word, so an
 * operator who sees a refusal before pressing Save reads the same words they
 * would have got back, and a test pins each against the Go source that says
 * it (core/internal/config/assurance.go). The browser refuses early only so
 * that a Save that cannot succeed is not offered; the server still decides.
 */

/** The values `builtin_check` takes. Empty is "structure" globally and
 *  "inherit" on a set, and is never a value a select offers. */
export const BUILTIN_CHECKS = ["structure", "off"] as const;
export type BuiltinCheck = (typeof BUILTIN_CHECKS)[number];

export const asBuiltinCheck = (v: string | undefined): BuiltinCheck => (v === "off" ? "off" : "structure");

/** `mode builtin` with `builtin_check: off`: the loader's refusal
 *  (config.validate: "mode %s runs the built-in structure check, so
 *  builtin_check: %s refuses it; set builtin_check to %s or choose another
 *  mode"). Null when the pair is fine. */
export function builtinOffProblem(mode: string, check: string): string | null {
  if (mode !== "builtin" || check !== "off") return null;
  return "mode builtin runs the built-in structure check, so builtin_check: off refuses it; set builtin_check to structure or choose another mode";
}

/** A validator id beginning `retnd-` (config.RecoveryTestScriptName: "%q is
 *  not usable: ids beginning %s are the product's own built-in checks"). */
export function reservedValidatorProblem(id: string): string | null {
  const v = id.trim();
  if (!v.startsWith("retnd-")) return null;
  return `"${v}" is not usable: ids beginning retnd- are the product's own built-in checks`;
}
