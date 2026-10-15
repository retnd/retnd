/**
 * The state behind a screen's recovery choices (issue #1190): which target is
 * chosen and the name typed or picked for it.
 *
 * Choosing another target always clears the name, because a folder under the
 * staging root and a destination id are different things and one carried
 * over into the other's field would be sent as the wrong kind. Each screen
 * used to write that rule out itself; here it lives once.
 *
 * A screen that offers the source host (issue #1191) starts with nothing
 * chosen (`initial` null) and works out its default once the engine has said
 * whether the source host can be used, so `kind` stays null until the
 * operator picks something.
 */
import { useCallback, useState } from "react";
import type { RecoveryKind } from "./recoveryPlaces";

export interface RecoveryTarget<T> {
  kind: T;
  where: string;
  setWhere(where: string): void;
  choose(next: Exclude<T, null>): void;
}

export function useRecoveryTarget<K extends RecoveryKind>(initial: K): RecoveryTarget<K>;
export function useRecoveryTarget<K extends RecoveryKind>(initial: K | null): RecoveryTarget<K | null>;
export function useRecoveryTarget<K extends RecoveryKind>(initial: K | null): RecoveryTarget<K | null> {
  const [kind, setKind] = useState<K | null>(initial);
  const [where, setWhere] = useState("");
  const choose = useCallback((next: K) => { setKind(next); setWhere(""); }, []);
  return { kind, where, setWhere, choose };
}
