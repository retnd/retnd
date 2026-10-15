/**
 * Asks the operator, in words, before a gated act goes over a connection that
 * is not encrypted (issue #1163).
 *
 * The engine refuses a gated act with INSECURE_TRANSPORT_ACK_REQUIRED when
 * the session cookie cannot be Secure on this connection (plain HTTP) and the
 * browser is not on the same machine as retnd. api/client.ts asks this dialog
 * and only a yes sends the request again with the acknowledgement. Mount it
 * once, near the root, beside the other app-wide dialogs.
 *
 * Two asks at once are one question: a screen that fires two gated requests
 * together gets one dialog, and both resolve with its answer.
 */
import { useCallback, useEffect, useRef, useState } from "react";

import { setInsecureTransportConfirmer } from "@shared/api/client";
import { ConfirmationDialog } from "@shared/components/ConfirmationDialog";

export function InsecureTransportPrompt() {
  const [open, setOpen] = useState(false);
  const waiting = useRef<Array<(answer: boolean) => void>>([]);

  const settle = useCallback((answer: boolean) => {
    const resolvers = waiting.current;
    waiting.current = [];
    setOpen(false);
    for (const resolve of resolvers) resolve(answer);
  }, []);

  useEffect(() => {
    setInsecureTransportConfirmer(
      () =>
        new Promise<boolean>((resolve) => {
          waiting.current.push(resolve);
          setOpen(true);
        })
    );
    return () => {
      setInsecureTransportConfirmer(null);
      // Unmounting while a question is open is a no.
      const resolvers = waiting.current;
      waiting.current = [];
      for (const resolve of resolvers) resolve(false);
    };
  }, []);

  return (
    <ConfirmationDialog
      open={open}
      eyebrow="Your session is not protected on this connection"
      title="This connection is not encrypted"
      confirmLabel="Continue anyway"
      // Drawn as the risky choice it is. The plain variant's eyebrow is a
      // third-tier grey that measures 3.35:1 on the dialog, under AA for
      // text this size; the destructive one is the danger colour.
      destructive
      onCancel={() => settle(false)}
      onConfirm={() => settle(true)}
    >
      <p style={{ margin: "0 0 10px" }}>
        {"You are reaching retnd over plain HTTP from another machine, so the page, your session and any " +
          "password you type travel across the network as readable text. Anyone on that network can copy them."}
      </p>
      <p style={{ margin: 0 }}>
        {"Putting HTTPS in front of retnd is the fix, and then you are not asked. If you are on a network you " +
          "trust and want to go ahead with this one action, continue."}
      </p>
    </ConfirmationDialog>
  );
}
