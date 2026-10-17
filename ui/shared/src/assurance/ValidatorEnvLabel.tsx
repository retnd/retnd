/**
 * A validator's environment in words (issue #1233): "Minimal environment" or
 * "Workflow environment, secrets included". The workflow one is the one that
 * needs care, so it is bold and set in a bordered chip on a tinted ground; the
 * words carry it, the chip only makes it easier to find in a table.
 */
import type { ValidatorEnv } from "@shared/api/contracts";
import { validatorEnvWords } from "./customValidators";

export function ValidatorEnvLabel({ env }: { env: ValidatorEnv }) {
  const words = validatorEnvWords(env);
  return (
    <span
      data-validator-env={words.cli}
      style={
        words.careful
          ? {
              display: "inline-block",
              fontWeight: 700,
              color: "var(--text)",
              background: "var(--warn-quiet)",
              border: "1px solid var(--warn)",
              borderRadius: "var(--radius-md)",
              padding: "1px 7px"
            }
          : { color: "var(--text)" }
      }
    >
      {words.label}
    </span>
  );
}
