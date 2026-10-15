/**
 * The state behind a screen's recovery choices (issue #1190): which target is
 * chosen and the name typed or picked for it.
 *
 * Choosing another target always clears the name, because a folder under the
 * staging root and a destination id are different things and one carried
 * over into the other's field would be sent as the wrong kind. Each screen
 * used to write that rule out itself; here it lives once.
 */
import { useCallback, useState } from "react";
import type { RecoveryKind } from "./recoveryPlaces";

export function useRecoveryTarget<K extends RecoveryKind>(initial: K) {
  const [kind, setKind] = useState<K>(initial);
  const [where, setWhere] = useState("");
  const choose = useCallback((next: K) => { setKind(next); setWhere(""); }, []);
  return { kind, where, setWhere, choose };
}
