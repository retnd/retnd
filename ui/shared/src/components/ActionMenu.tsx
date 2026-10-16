/**
 * A menu button: one trigger, one pop-up menu of commands (issue #1202).
 *
 * The product had no menu before this, so it follows the WAI-ARIA menu button
 * pattern rather than a house variant of it. The trigger is a button with
 * `aria-haspopup="menu"` and `aria-expanded`; Enter, Space or ArrowDown open
 * the menu on its first item and ArrowUp on its last; inside it the arrows
 * move and wrap, Home and End jump, a letter jumps to the next item starting
 * with it, Escape closes and puts focus back on the trigger, and Tab closes
 * and lets focus move on. A click outside closes it.
 *
 * An item that cannot run stays in the menu. It is `aria-disabled` rather than
 * `disabled`, so the arrows still reach it and a screen reader still reads it,
 * and its reason takes the place of its description and is the item's
 * `aria-describedby`. Pressing it does nothing. Hiding it would leave the
 * operator wondering where the command went.
 *
 * The pop-up is portalled to <body> and fixed to the viewport, for the reason
 * tooltips/popover.ts gives: the tables it hangs off scroll sideways and every
 * card clips. React still bubbles a portal's events through the component
 * tree, so the pop-up stops its clicks, key presses and context menus there,
 * or a row underneath would read them as its own.
 *
 * The footer prints the terminal twin of whichever item has focus, from the
 * caller's command builders. It is drawn as CommandEcho draws a line, without
 * CommandEcho's tooltip host, because a tooltip trigger inside a menu would be
 * a focus stop the arrow keys never reach.
 */
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, RefObject } from "react";
import { createPortal } from "react-dom";

export interface ActionMenuItem {
  id: string;
  label: string;
  /** One line saying what the item does. */
  description?: string;
  /** Why it cannot run now. Non-null makes the item disabled. */
  disabledReason?: string | null;
  /** The same thing from the terminal. */
  echo?: string;
  onSelect(): void;
}

export interface ActionMenuGroup {
  label: string;
  items: ActionMenuItem[];
}

export type OpenAt = "first" | "last";

/** Below the trigger by preference, its right edge on the trigger's, flipped
 *  above when there is no room below, and clamped into the viewport. A menu
 *  reads top-down from what opened it, so below comes first, unlike a
 *  tooltip (tooltips/popover.ts), which avoids covering what is read next. */
function placeMenu(
  anchor: { top: number; bottom: number; right: number },
  size: { width: number; height: number },
  viewport: { width: number; height: number }
): { top: number; left: number } {
  const GAP = 4, MARGIN = 8;
  const below = anchor.bottom + GAP;
  const above = anchor.top - GAP - size.height;
  const top = below + size.height <= viewport.height - MARGIN ? below : above >= MARGIN ? above : below;
  const clamp = (v: number, limit: number) => Math.max(MARGIN, Math.min(v, limit - MARGIN));
  return { top: clamp(top, viewport.height - size.height), left: clamp(anchor.right - size.width, viewport.width - size.width) };
}

export function ActionMenu({
  label, triggerText = "Actions", groups, open, openAt = "first", onOpen, onClose, buttonRef, footerLabel = "Same thing from the terminal"
}: {
  /** The trigger's accessible name and the menu's. */
  label: string;
  triggerText?: string;
  groups: ActionMenuGroup[];
  open: boolean;
  openAt?: OpenAt;
  onOpen(at: OpenAt): void;
  onClose(): void;
  buttonRef?: (el: HTMLButtonElement | null) => void;
  footerLabel?: string;
}) {
  const trigger = useRef<HTMLButtonElement | null>(null);
  const menuId = useId();
  return (
    <>
      <button
        ref={(el) => { trigger.current = el; buttonRef?.(el); }}
        type="button"
        className="btn btn--sm btn--strong-edge amenu__trigger"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={(e) => { e.stopPropagation(); if (open) onClose(); else onOpen("first"); }}
        onKeyDown={(e) => {
          if (e.key === "ArrowDown") { e.preventDefault(); e.stopPropagation(); onOpen("first"); }
          else if (e.key === "ArrowUp") { e.preventDefault(); e.stopPropagation(); onOpen("last"); }
        }}
      >
        {triggerText}
        <span aria-hidden="true" className="amenu__caret">{"▾"}</span>
      </button>
      {open ? (
        <MenuPopup id={menuId} label={label} groups={groups} openAt={openAt} trigger={trigger} onClose={onClose} footerLabel={footerLabel} />
      ) : null}
    </>
  );
}

function MenuPopup({
  id, label, groups, openAt, trigger, onClose, footerLabel
}: {
  id: string;
  label: string;
  groups: ActionMenuGroup[];
  openAt: OpenAt;
  trigger: RefObject<HTMLButtonElement | null>;
  onClose(): void;
  footerLabel: string;
}) {
  const box = useRef<HTMLDivElement>(null);
  const items = groups.flatMap((g) => g.items);
  const [active, setActive] = useState(() => (openAt === "last" ? items.length - 1 : 0));
  const [place, setPlace] = useState<{ top: number; left: number } | null>(null);
  const close = useRef(onClose);
  useEffect(() => { close.current = onClose; });

  // Placed before paint, and again whenever the page under it moves.
  useLayoutEffect(() => {
    const at = () => {
      const t = trigger.current, m = box.current;
      if (!t || !m) return;
      setPlace(placeMenu(t.getBoundingClientRect(), { width: m.offsetWidth, height: m.offsetHeight }, { width: window.innerWidth, height: window.innerHeight }));
    };
    at();
    window.addEventListener("resize", at);
    window.addEventListener("scroll", at, true);
    return () => { window.removeEventListener("resize", at); window.removeEventListener("scroll", at, true); };
  }, [trigger]);

  // Roving focus: the active item holds it. Re-run once placed, because the
  // first pass runs while the pop-up is still hidden for measuring.
  useEffect(() => {
    box.current?.querySelectorAll<HTMLElement>('[role="menuitem"]')[active]?.focus();
  }, [active, place]);

  // A press anywhere else closes it. Focus is not pulled back to the trigger:
  // the press already put it where the operator wanted it.
  useEffect(() => {
    const down = (e: PointerEvent) => {
      const t = e.target as Node;
      if (box.current?.contains(t) || trigger.current?.contains(t)) return;
      close.current();
    };
    document.addEventListener("pointerdown", down, true);
    return () => document.removeEventListener("pointerdown", down, true);
  }, [trigger]);

  const back = () => { trigger.current?.focus(); close.current(); };
  const choose = (item: ActionMenuItem) => {
    if (item.disabledReason) return;
    back();
    item.onSelect();
  };

  const onKeyDown = (e: ReactKeyboardEvent) => {
    e.stopPropagation();
    const n = items.length;
    if (e.key === "ArrowDown") { e.preventDefault(); setActive((i) => (i + 1) % n); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setActive((i) => (i - 1 + n) % n); }
    else if (e.key === "Home") { e.preventDefault(); setActive(0); }
    else if (e.key === "End") { e.preventDefault(); setActive(n - 1); }
    else if (e.key === "Escape") { e.preventDefault(); back(); }
    else if (e.key === "Tab") {
      // Focus goes back to the trigger first, so the Tab the browser is about
      // to carry out moves on from the row rather than from the end of <body>.
      trigger.current?.focus();
      close.current();
    } else if (e.key === "Enter" || e.key === " ") { e.preventDefault(); choose(items[active]); }
    else if (e.key.length === 1 && /\S/.test(e.key) && !e.ctrlKey && !e.metaKey && !e.altKey) {
      const k = e.key.toLowerCase();
      for (let step = 1; step <= n; step++) {
        const i = (active + step) % n;
        if (items[i].label.toLowerCase().startsWith(k)) { setActive(i); break; }
      }
    }
  };

  const echo = items[active]?.echo;
  // Each group's first item's position in the flat list the keys walk.
  const starts = groups.map((_, g) => groups.slice(0, g).reduce((n, x) => n + x.items.length, 0));
  return createPortal(
    <div
      ref={box}
      id={id}
      role="menu"
      aria-label={label}
      className="amenu"
      data-assurance=""
      style={place ? { top: place.top, left: place.left } : { top: 0, left: 0, visibility: "hidden" }}
      onKeyDown={onKeyDown}
      onClick={(e) => e.stopPropagation()}
      onContextMenu={(e) => { e.preventDefault(); e.stopPropagation(); }}
    >
      {groups.map((g, gi) => (
        <div key={g.label} role="group" aria-labelledby={id + "-g-" + gi} className="amenu__group">
          <div id={id + "-g-" + gi} role="presentation" className="amenu__group-label">{g.label}</div>
          {g.items.map((item, k) => {
            const i = starts[gi] + k;
            const labelId = id + "-" + item.id;
            const why = item.disabledReason ?? null;
            const note = why ?? item.description;
            return (
              <div
                key={item.id}
                role="menuitem"
                tabIndex={i === active ? 0 : -1}
                aria-labelledby={labelId}
                aria-describedby={note ? labelId + "-note" : undefined}
                aria-disabled={why ? "true" : undefined}
                className="amenu__item"
                data-item={item.id}
                onClick={() => choose(item)}
                onFocus={() => { if (i !== active) setActive(i); }}
                onPointerMove={() => { if (i !== active) setActive(i); }}
              >
                <span id={labelId} className="amenu__label">{item.label}</span>
                {note ? <span id={labelId + "-note"} className={why ? "amenu__why" : "amenu__note"}>{note}</span> : null}
              </div>
            );
          })}
        </div>
      ))}
      {echo ? (
        <div className="amenu__footer" data-testid="menu-echo">
          <div className="eyebrow" style={{ fontSize: 10 }}>{footerLabel}</div>
          <code className="mono amenu__echo">{echo}</code>
        </div>
      ) : null}
    </div>,
    document.body
  );
}
