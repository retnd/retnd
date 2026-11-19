/**
 * Issue #1233: the words a validator's environment gets in the browser.
 *
 * `retnd assurance validators list` prints "env minimal" or "env workflow"
 * (core/cmd/retnd/assurancevalidators.go, printCustomValidator), and
 * `--env` takes the same two words. The browser's sentence has to start from
 * those words so the terminal and the page tell one story, and the workflow
 * one has to say that it carries secrets.
 */
import { describe, expect, it } from "vitest";
import { validatorEnvWords } from "./customValidators";

describe("validatorEnvWords", () => {
  it("uses the words the issue asks for as each environment's label", () => {
    expect(validatorEnvWords("minimal").label).toBe("Minimal environment");
    expect(validatorEnvWords("workflow").label).toBe("Workflow environment, secrets included");
  });

  it("carries the CLI's own word for each, as `validators list` prints it", () => {
    expect(validatorEnvWords("minimal").cli).toBe("minimal");
    expect(validatorEnvWords("workflow").cli).toBe("workflow");
    for (const env of ["minimal", "workflow"] as const) {
      expect(validatorEnvWords(env).label.toLowerCase()).toContain(validatorEnvWords(env).cli);
    }
  });

  it("marks only the workflow environment as the one that needs care", () => {
    expect(validatorEnvWords("minimal").careful).toBe(false);
    expect(validatorEnvWords("workflow").careful).toBe(true);
  });

  it("explains what each one exposes, and says secrets for the workflow one", () => {
    const minimal = validatorEnvWords("minimal").sentence;
    expect(minimal).toContain("RECOVERY_TEST_");
    expect(minimal).toContain("PATH");
    const workflow = validatorEnvWords("workflow").sentence;
    expect(workflow).toContain("workflow");
    expect(workflow).toContain("secrets");
    expect(workflow.match(/\./g)?.length).toBe(1);
  });

  it("never leans on colour or punctuation alone: letters only, no em dash", () => {
    for (const env of ["minimal", "workflow"] as const) {
      const w = validatorEnvWords(env);
      expect(w.label).toMatch(/[A-Za-z]{4}/);
      expect(w.label + w.sentence).not.toContain("—");
    }
  });
});
