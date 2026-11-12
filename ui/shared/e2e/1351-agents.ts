/** Shared by the #1351 specs: the card, and the scenarios of agents/mockAgents.ts. */
import type { Page } from "@playwright/test";
import { open } from "./helpers";

export const PASSWORD = "correct-horse-battery";
/** MOCK_MINTED_SECRET in src/agents/mockAgents.ts. It opens nothing. */
export const SECRET = "rat_EXAMPLE0000000000000000000000000000000000";

export const card = (page: Page) => page.getByRole("region", { name: "Agents", exact: true });
export const region = (page: Page, name: "tokens" | "confirmations" | "audit") =>
  card(page).locator('[data-agents-region="' + name + '"]');
export const tokenRow = (page: Page, name: string) => card(page).locator('[data-token="' + name + '"]');
export const pending = (page: Page, id: string) => card(page).locator('[data-confirmation="' + id + '"]');

export async function openAgents(page: Page, scenario = "agents-tokens") {
  await open(page, "/settings", scenario);
  await card(page).waitFor();
}

/** Opens the Create token dialog and returns it. */
export async function openMint(page: Page) {
  await region(page, "tokens").getByRole("button", { name: "Create token" }).click();
  return page.getByRole("dialog", { name: "Create token" });
}
