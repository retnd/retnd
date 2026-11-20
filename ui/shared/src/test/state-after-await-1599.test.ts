/**
 * Issue #1599: no state setter runs after an await unless something checks
 * the component is still there.
 *
 * That's the hole #1589 fell into. A handler or an effect awaits a request,
 * the component unmounts while it's out, and the answer calls a setter
 * anyway. In a browser React drops the update. In vitest it reads
 * `window.event` first, which throws once jsdom has been torn down, and the
 * promise the component voided rejects with nobody listening. #1590 and
 * #1597 fixed the ones a test had tripped over; this holds every other site
 * to the same rule, so a new handler can't bring the hole back.
 *
 * Every .ts and .tsx under src that ships is parsed with TypeScript. A
 * setter is the second name out of `useState` or `useReducer`, or a `set*`
 * name destructured from another hook's answer, in the component or hook
 * that declares it, and so is a plain function there that calls one
 * straight away (`const forget = () => { ...; setHasPassword(false); }`). A call to one is "after an await" when an `await` comes
 * before it in the same async function (or in a loop that awaits), or when
 * it sits in a `.then`, `.catch` or `.finally` callback. A setter handed
 * straight to `.then(setX)` can never be guarded, so it always counts.
 *
 * Such a call is guarded when, after the last await before it, one of these
 * stands between the await and the call:
 *   - `if (!mountedRef.current) return;` (or throw, break, continue), where
 *     `mounted` comes from useMounted();
 *   - `if (mountedRef.current) setX(...)`, `mountedRef.current && setX(...)` or a
 *     `?:` on it;
 *   - the same with an effect's own flag: a `let` (or a ref) its cleanup
 *     sets, read the right way round (`live` set false, `cancelled` set
 *     true), or the `.aborted` of a signal a cleanup aborts.
 * A zero-argument arrow that returns such a check (`const current = () =>
 * mountedRef.current && mine === asked.current`) counts as the check. The
 * direction is checked too, so `if (mountedRef.current) return;` guards
 * nothing. A newest-answer counter on its own isn't a guard: a ref keeps its
 * value after unmount.
 *
 * What it can't see, and doesn't pretend to: a setter a parent passed down
 * (the parent may well still be mounted), and a callback some API calls
 * later without an await or a `.then` in sight (a stream's onProgress, a
 * subscription). Those are a different shape of the same question and are
 * left to the probe tests.
 *
 * ALLOWED lists the sites that stay unguarded on purpose, each with the
 * reason. An entry that no longer matches a site fails the run, so the list
 * can't quietly outlive what it excused.
 */
import ts from "typescript";
import { describe, expect, it } from "vitest";

const sources = import.meta.glob<string>(["../**/*.{ts,tsx}", "!../test/**", "!../**/*.test.{ts,tsx}"], {
  query: "?raw",
  import: "default",
  eager: true
});

/** `file › Component.function › setter`, with the reason it stays as it is. */
const ALLOWED: Record<string, string> = {
  "agents/ConfirmationsRegion.tsx › ConfirmationRow.approve › onApproved":
    "The decision already happened. onApproved is the region's handler, which checks the region's own useMounted() before it touches anything, so a row that went while the region stays still reports it.",
  "agents/ConfirmationsRegion.tsx › ConfirmationRow.deny › onDenied":
    "Same as approve: the denial already happened, and the region's handler checks the region's own useMounted() first.",
  "assurance/CustomValidatorDialog.tsx › CustomValidatorDialog.save › onSaved":
    "The validator is already saved. onSaved is the card's handler, which checks the card's own useMounted() first, so a dialog that went while the card stays still closes and reloads the list.",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.handleSave › onFirstRunComplete":
    "The set is saved, which ends first run whether or not the wizard is still on screen. onFirstRunComplete is App's handler and App is always mounted, so it sets no state on a gone component.",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.save › onSaved":
    "The destination is already saved. onSaved is the card's handler, which checks the card's own useMounted() first.",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.save › onSaved":
    "The destination is already saved. onSaved is the card's handler, which checks the card's own useMounted() first.",
  "pages/StorageDestinationsCard.tsx › DestinationRow.makeDefault › onChanged":
    "The change already happened. onChanged is the card's changed(), which checks the card's own useMounted() first.",
  "pages/StorageDestinationsCard.tsx › DestinationRow.remove › onChanged":
    "The removal already happened. onChanged is the card's changed(), which checks the card's own useMounted() first."
};

/**
 * The sites still waiting for their batch. #1599 is fixed in three stacked
 * PRs (the hook with agents/, assurance/ and hooks/; then pages/; then the
 * rest), and every commit has to pass, so this list holds what the later
 * batches haven't reached yet. Each batch takes its own sites off it, an
 * entry that no longer matches a site fails the run, and the last batch
 * deletes the list.
 */
const PENDING = new Set<string>([
  "App.tsx › App.onSkip › setFirstRun",
  "auth/EnrollmentPage.tsx › EnrollmentPage.submit › onEnrolled",
  "auth/EnrollmentPage.tsx › EnrollmentPage.submit › setBusy",
  "auth/EnrollmentPage.tsx › EnrollmentPage.submit › setFailure",
  "auth/ForgotPasswordPage.tsx › ForgotPasswordPage.submit › setAsked",
  "auth/ForgotPasswordPage.tsx › ForgotPasswordPage.submit › setBusy",
  "auth/ForgotPasswordPage.tsx › ForgotPasswordPage.submit › setFailure",
  "auth/LoginPage.tsx › SignInForm.submit › onSignedIn",
  "auth/LoginPage.tsx › SignInForm.submit › setBusy",
  "auth/LoginPage.tsx › SignInForm.submit › setFailure",
  "auth/ResetPasswordPage.tsx › ResetPasswordPage.submit › setBusy",
  "auth/ResetPasswordPage.tsx › ResetPasswordPage.submit › setConfirm",
  "auth/ResetPasswordPage.tsx › ResetPasswordPage.submit › setDone",
  "auth/ResetPasswordPage.tsx › ResetPasswordPage.submit › setFailure",
  "auth/ResetPasswordPage.tsx › ResetPasswordPage.submit › setPassword",
  "components/ActivityDock.tsx › ActivityDock.copy › setCopied",
  "components/EmptyState.tsx › ErrorState.onClick › setCopied",
  "components/FirstRunGuide.tsx › FirstRunGuide.onClick › setBusy",
  "components/RecoveryVerificationBanner.tsx › RecoveryVerificationBanner.resend › setFailure",
  "components/RecoveryVerificationBanner.tsx › RecoveryVerificationBanner.resend › setResending",
  "components/RecoveryVerificationBanner.tsx › RecoveryVerificationBanner.resend › setResent",
  "components/RemoveBackupSetDialog.tsx › RemoveBackupSetDialog.onConfirm › onRemoved",
  "components/RemoveBackupSetDialog.tsx › RemoveBackupSetDialog.onConfirm › setError",
  "components/RemoveBackupSetDialog.tsx › RemoveBackupSetDialog.onConfirm › setRemoving",
  "components/SSHAuthWizard.tsx › OpenWizard.apply › onApplied",
  "components/SSHAuthWizard.tsx › OpenWizard.apply › setBusy",
  "components/SSHAuthWizard.tsx › OpenWizard.apply › setError",
  "components/SSHAuthWizard.tsx › OpenWizard.apply › setStep",
  "components/SSHAuthWizard.tsx › OpenWizard.importPasted › setBusy",
  "components/SSHAuthWizard.tsx › OpenWizard.importPasted › setChosen",
  "components/SSHAuthWizard.tsx › OpenWizard.importPasted › setError",
  "components/SSHAuthWizard.tsx › OpenWizard.importPasted › setOutcome",
  "components/SSHAuthWizard.tsx › OpenWizard.importPasted › setPastedKey",
  "components/SSHAuthWizard.tsx › OpenWizard.importPasted › setPastedPassphrase",
  "components/SSHAuthWizard.tsx › OpenWizard.importPasted › setReloadToken",
  "components/SSHAuthWizard.tsx › OpenWizard.importPasted › setVerifiedAgainst",
  "components/SSHAuthWizard.tsx › OpenWizard.probe › setAckedFingerprint",
  "components/SSHAuthWizard.tsx › OpenWizard.probe › setBusy",
  "components/SSHAuthWizard.tsx › OpenWizard.probe › setError",
  "components/SSHAuthWizard.tsx › OpenWizard.probe › setOutcome",
  "components/SSHAuthWizard.tsx › OpenWizard.probe › setProbed",
  "components/SSHAuthWizard.tsx › OpenWizard.probe › setVerifiedAgainst",
  "components/SSHAuthWizard.tsx › OpenWizard.selectCandidate › setBusy",
  "components/SSHAuthWizard.tsx › OpenWizard.selectCandidate › setChosen",
  "components/SSHAuthWizard.tsx › OpenWizard.selectCandidate › setError",
  "components/SSHAuthWizard.tsx › OpenWizard.selectCandidate › setOutcome",
  "components/SSHAuthWizard.tsx › OpenWizard.selectCandidate › setReloadToken",
  "components/SSHAuthWizard.tsx › OpenWizard.selectCandidate › setVerifiedAgainst",
  "components/SSHAuthWizard.tsx › OpenWizard.verify › setBusy",
  "components/SSHAuthWizard.tsx › OpenWizard.verify › setError",
  "components/SSHAuthWizard.tsx › OpenWizard.verify › setOutcome",
  "components/SSHAuthWizard.tsx › OpenWizard.verify › setVerifiedAgainst",
  "components/StepLogTerminal.tsx › StepLogTerminal.copy › setCopied",
  "components/WorkflowEnvironmentEditor.tsx › WorkflowEnvironmentEditor.remove › setBusy",
  "components/WorkflowEnvironmentEditor.tsx › WorkflowEnvironmentEditor.remove › setFailure",
  "components/WorkflowEnvironmentEditor.tsx › WorkflowEnvironmentEditor.save › setBusy",
  "components/WorkflowEnvironmentEditor.tsx › WorkflowEnvironmentEditor.save › setDraftName",
  "components/WorkflowEnvironmentEditor.tsx › WorkflowEnvironmentEditor.save › setDraftValue",
  "components/WorkflowEnvironmentEditor.tsx › WorkflowEnvironmentEditor.save › setFailure",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.acknowledge › onSettled",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.acknowledge › setAcknowledging",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.acknowledge › setBusy",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.acknowledge › setFailure",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.acknowledge › setReason",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.resume › onSettled",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.resume › setBusy",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.resume › setFailure",
  "components/stepterminal/stream.ts › pump › onChange",
  "components/stepterminal/stream.ts › start › onChange",
  "encryption/BundleDialog.tsx › BundleDialog.download › forget",
  "encryption/BundleDialog.tsx › BundleDialog.download › onDone",
  "encryption/BundleDialog.tsx › BundleDialog.download › setBusy",
  "encryption/BundleDialog.tsx › BundleDialog.download › setError",
  "encryption/ClippedText.tsx › ClippedText.copy › setCopied",
  "encryption/EncryptionCard.tsx › EncryptionCard.act › setBusy",
  "encryption/EncryptionCard.tsx › EncryptionCard.act › setFailure",
  "encryption/EncryptionCard.tsx › EncryptionCard.onConfirm › close",
  "encryption/EncryptionCard.tsx › EncryptionCard.onEnable › close",
  "encryption/EncryptionCard.tsx › EncryptionCard.onRetire › close",
  "encryption/EncryptionCard.tsx › EncryptionCard.onSave › close",
  "encryption/ExternalReferenceFields.tsx › ExternalReferenceFields.test › setProbe",
  "encryption/ExternalReferenceFields.tsx › ExternalReferenceFields.test › setProbeFailure",
  "encryption/ExternalReferenceFields.tsx › ExternalReferenceFields.test › setTesting",
  "encryption/ReencryptDialog.tsx › ReencryptDialog.run › setBusy",
  "encryption/ReencryptDialog.tsx › ReencryptDialog.run › setFailure",
  "encryption/ReencryptDialog.tsx › ReencryptDialog.run › setStarted",
  "encryption/SetEncryptionCard.tsx › SetEncryptionCard.change › setBusy",
  "encryption/SetEncryptionCard.tsx › SetEncryptionCard.change › setFailure",
  "encryption/SetEncryptionCard.tsx › SetEncryptionCard.onClick › setDraft",
  "encryption/SetEncryptionCard.tsx › SetEncryptionCard.onClick › setOwnKey",
  "encryption/SuiteControls.tsx › SuiteChangeFlow.onConfirm › onChanged",
  "encryption/SuiteControls.tsx › SuiteChangeFlow.onConfirm › onClose",
  "encryption/SuiteControls.tsx › SuiteChangeFlow.onConfirm › setKeyId",
  "encryption/SuiteControls.tsx › SuiteChangeFlow.onConfirm › setStep",
  "encryption/SuiteControls.tsx › SuiteChangeFlow.run › setBusy",
  "encryption/SuiteControls.tsx › SuiteChangeFlow.run › setFailure",
  "encryption/VerifyRecoverableDialog.tsx › VerifyRecoverableDialog.start › setBusy",
  "encryption/VerifyRecoverableDialog.tsx › VerifyRecoverableDialog.start › setOpId",
  "encryption/VerifyRecoverableDialog.tsx › VerifyRecoverableDialog.start › setRefusal",
  "observability/AlertDeliveryControls.tsx › AlertDeliveryControls.save › onSaved",
  "observability/AlertDeliveryControls.tsx › AlertDeliveryControls.save › setBusy",
  "observability/AlertDeliveryControls.tsx › AlertDeliveryControls.save › setError",
  "observability/LogLevelControl.tsx › LogLevelControl.change › onSaved",
  "observability/LogLevelControl.tsx › LogLevelControl.change › setProblem",
  "observability/LogLevelControl.tsx › LogLevelControl.change › setSaving",
  "observability/ObservabilityCard.tsx › ObservabilityCard.apply › setBusy",
  "observability/ObservabilityCard.tsx › ObservabilityCard.apply › setError",
  "observability/ObservabilityCard.tsx › ObservabilityCard.apply › setInterval",
  "observability/ObservabilityCard.tsx › ObservabilityCard.apply › setSettings",
  "observability/ObservabilityCard.tsx › ObservabilityCard.mint › setBusy",
  "observability/ObservabilityCard.tsx › ObservabilityCard.mint › setCopied",
  "observability/ObservabilityCard.tsx › ObservabilityCard.mint › setError",
  "observability/ObservabilityCard.tsx › ObservabilityCard.mint › setMinted",
  "observability/ObservabilityCard.tsx › ObservabilityCard.mint › setSettings",
  "observability/ObservabilityCard.tsx › ObservabilityCard.onClick › setCopied",
]);

interface Site {
  file: string;
  line: number;
  key: string;
}

type Fn = ts.FunctionDeclaration | ts.FunctionExpression | ts.ArrowFunction | ts.MethodDeclaration;

/** Where control came back to the function a setter call is in: the function,
 *  the position, the await when it was one, and the call (or the nested
 *  function holding it) as seen from that function. */
interface Origin {
  fn: Fn;
  from: number;
  at?: ts.Node;
  call: ts.Node;
}

interface Scope {
  setters: Set<string>;
  /** Expressions that are true while the component is there. */
  alive: Set<string>;
  /** Expressions that are true once it has gone. */
  dead: Set<string>;
  aborts: boolean;
  /** Callbacks a parent handed down: `on*` and `set*` props (#1599 review). */
  callbacks: Set<string>;
  /** Refs that hold a setter: `useRef(setX)` or `ref.current = setX`. */
  refSetters: Set<string>;
  /** Zero-argument arrows with an expression body, by name. */
  checks: Map<string, ts.Expression>;
}

const PROMISE_CALLBACKS = new Set(["then", "catch", "finally"]);
const EFFECTS = new Set(["useEffect", "useLayoutEffect"]);

function isFn(n: ts.Node): n is Fn {
  return ts.isFunctionDeclaration(n) || ts.isFunctionExpression(n) || ts.isArrowFunction(n) || ts.isMethodDeclaration(n);
}

function isAsync(fn: Fn): boolean {
  return (ts.getCombinedModifierFlags(fn) & ts.ModifierFlags.Async) !== 0;
}

function enclosingFn(n: ts.Node): Fn | undefined {
  for (let p = n.parent; p; p = p.parent) if (isFn(p)) return p;
  return undefined;
}

function calleeName(call: ts.CallExpression): string | undefined {
  const e = call.expression;
  if (ts.isIdentifier(e)) return e.text;
  if (ts.isPropertyAccessExpression(e)) return e.name.text;
  return undefined;
}

function isPromiseCallback(fn: ts.Node): boolean {
  const call = fn.parent;
  return (
    !!call &&
    ts.isCallExpression(call) &&
    call.arguments.includes(fn as ts.Expression) &&
    ts.isPropertyAccessExpression(call.expression) &&
    PROMISE_CALLBACKS.has(call.expression.name.text)
  );
}

function unparen(e: ts.Expression): ts.Expression {
  while (ts.isParenthesizedExpression(e)) e = e.expression;
  return e;
}

/** `x` or `x.current`, the two shapes a flag is written in. */
function flagText(e: ts.Expression): string | undefined {
  e = unparen(e);
  if (ts.isIdentifier(e)) return e.text;
  if (ts.isPropertyAccessExpression(e) && ts.isIdentifier(e.expression) && e.name.text === "current") return e.expression.text + ".current";
  return undefined;
}

/** The name a function is declared under: `function Card`, `const Card = () =>`. */
function declaredName(fn: Fn): string | undefined {
  if ((ts.isFunctionDeclaration(fn) || ts.isFunctionExpression(fn)) && fn.name) return fn.name.text;
  let up: ts.Node = fn.parent;
  // `const Card = memo(forwardRef(() => ...))`
  while (ts.isParenthesizedExpression(up) || ts.isCallExpression(up)) up = up.parent;
  return ts.isVariableDeclaration(up) && ts.isIdentifier(up.name) ? up.name.text : undefined;
}

function analyse(file: string, text: string, shared: ReadonlySet<string>): { sites: Site[]; props: Set<string> } {
  const kind = file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, kind);
  const scopes = new Map<ts.Node, Scope>();
  const scopeOf = (n: ts.Node): Scope => {
    const owner = enclosingFn(n) ?? sf;
    let s = scopes.get(owner);
    if (!s) scopes.set(owner, (s = { setters: new Set(), alive: new Set(), dead: new Set(), aborts: false, callbacks: new Set(), refSetters: new Set(), checks: new Map() }));
    return s;
  };

  // Plain (not async) functions a component declares, which may set state
  // themselves: `const forget = () => { ...; setHasPassword(false); }`.
  const helpers: { scope: Scope; name: string; fn: Fn }[] = [];
  // An async one counts too: what it does before its first await runs
  // synchronously inside whoever calls it (#1599 review).
  const helper = (n: ts.Node, name: string, fn: ts.Node) => {
    if (isFn(fn) && enclosingFn(n)) helpers.push({ scope: scopeOf(n), name, fn });
  };
  // `done: () => { ...; setX(false); }` in an object a hook returns: called
  // as `source.done()`, so it is known by its property name, in every file.
  const propHelpers: { name: string; fn: Fn }[] = [];
  // `const alias = setX;`
  const aliases: { scope: Scope; name: string; target: string }[] = [];
  // `useRef(setX)` and `ref.current = setX`: called as `ref.current(...)`.
  const refs: { scope: Scope; name: string; target: string }[] = [];

  // First pass: what each function declares.
  const declare = (n: ts.Node) => {
    if (ts.isFunctionDeclaration(n) && n.name) helper(n, n.name.text, n);
    if (ts.isVariableDeclaration(n) && n.initializer && ts.isIdentifier(n.name)) {
      const init = unparen(n.initializer);
      if (ts.isCallExpression(init) && calleeName(init) === "useCallback" && init.arguments[0]) helper(n, n.name.text, init.arguments[0]);
      else helper(n, n.name.text, init);
    }
    if (ts.isPropertyAssignment(n) && ts.isObjectLiteralExpression(n.parent)) {
      const v = unparen(n.initializer);
      if (isFn(v)) propHelpers.push({ name: n.name.getText(sf), fn: v });
    }
    if (ts.isMethodDeclaration(n) && ts.isObjectLiteralExpression(n.parent)) propHelpers.push({ name: n.name.getText(sf), fn: n });
    // What a parent handed down: `{ onSaved, setItems }` or `onSaved` as a parameter.
    if (ts.isParameter(n) && isFn(n.parent) && /^([A-Z]|use[A-Z])/.test(declaredName(n.parent) ?? "")) {
      const scope = scopeOf(n);
      if (ts.isIdentifier(n.name) && /^on[A-Z]/.test(n.name.text)) scope.callbacks.add(n.name.text);
      if (ts.isObjectBindingPattern(n.name)) {
        for (const el of n.name.elements) {
          if (ts.isIdentifier(el.name) && /^(on|set)[A-Z]/.test(el.name.text)) scope.callbacks.add(el.name.text);
        }
      }
    }
    if (ts.isVariableDeclaration(n) && n.initializer) {
      const init = unparen(n.initializer);
      if (ts.isIdentifier(n.name) && ts.isIdentifier(init)) aliases.push({ scope: scopeOf(n), name: n.name.text, target: init.text });
      if (ts.isObjectBindingPattern(n.name) && ts.isIdentifier(init) && init.text === "props") {
        for (const el of n.name.elements) {
          if (ts.isIdentifier(el.name) && /^(on|set)[A-Z]/.test(el.name.text)) scopeOf(n).callbacks.add(el.name.text);
        }
      }
      if (ts.isCallExpression(init)) {
        const callee = calleeName(init);
        if (ts.isArrayBindingPattern(n.name) && (callee === "useState" || callee === "useReducer")) {
          const second = n.name.elements[1];
          if (second && ts.isBindingElement(second) && ts.isIdentifier(second.name)) scopeOf(n).setters.add(second.name.text);
        } else if (ts.isArrayBindingPattern(n.name) && callee && /^use[A-Z]/.test(callee)) {
          for (const el of n.name.elements) {
            if (ts.isBindingElement(el) && ts.isIdentifier(el.name) && /^set[A-Z]/.test(el.name.text)) scopeOf(n).setters.add(el.name.text);
          }
        } else if (ts.isObjectBindingPattern(n.name) && callee && /^use[A-Z]/.test(callee)) {
          for (const el of n.name.elements) {
            if (ts.isIdentifier(el.name) && /^set[A-Z]/.test(el.name.text)) scopeOf(n).setters.add(el.name.text);
          }
        } else if (ts.isIdentifier(n.name) && callee === "useRef" && init.arguments[0] && ts.isIdentifier(unparen(init.arguments[0]))) {
          refs.push({ scope: scopeOf(n), name: n.name.text, target: (unparen(init.arguments[0]) as ts.Identifier).text });
        } else if (ts.isIdentifier(n.name) && callee === "useMounted") {
          scopeOf(n).alive.add(n.name.text + ".current");
        }
      } else if (ts.isIdentifier(n.name) && ts.isArrowFunction(init) && init.parameters.length === 0 && !ts.isBlock(init.body)) {
        scopeOf(n).checks.set(n.name.text, init.body);
      }
    }
    if (ts.isBinaryExpression(n) && n.operatorToken.kind === ts.SyntaxKind.EqualsToken && ts.isIdentifier(unparen(n.right))) {
      const left = n.left;
      if (ts.isPropertyAccessExpression(left) && left.name.text === "current" && ts.isIdentifier(left.expression)) {
        refs.push({ scope: scopeOf(n), name: left.expression.text, target: (unparen(n.right) as ts.Identifier).text });
      }
    }
    if (ts.isCallExpression(n) && EFFECTS.has(calleeName(n) ?? "") && n.arguments[0] && isFn(n.arguments[0])) {
      const effect = n.arguments[0];
      const scope = scopeOf(n);
      const cleanups: ts.Node[] = [];
      const findReturns = (m: ts.Node) => {
        if (ts.isReturnStatement(m) && m.expression && isFn(unparen(m.expression))) cleanups.push(unparen(m.expression));
        if (!isFn(m)) ts.forEachChild(m, findReturns);
      };
      ts.forEachChild(effect.body, findReturns);
      for (const c of cleanups) {
        const assigns = (m: ts.Node) => {
          if (ts.isBinaryExpression(m) && m.operatorToken.kind === ts.SyntaxKind.EqualsToken) {
            const flag = flagText(m.left);
            if (flag && m.right.kind === ts.SyntaxKind.FalseKeyword) scope.alive.add(flag);
            if (flag && m.right.kind === ts.SyntaxKind.TrueKeyword) scope.dead.add(flag);
          }
          if (ts.isCallExpression(m) && calleeName(m) === "abort") scope.aborts = true;
          ts.forEachChild(m, assigns);
        };
        assigns(c);
      }
    }
    ts.forEachChild(n, declare);
  };
  declare(sf);

  /** Everything a node can see: its own function's scope and every one around it. */
  const visible = (n: ts.Node): Scope => {
    const out: Scope = { setters: new Set(), alive: new Set(), dead: new Set(), aborts: false, callbacks: new Set(), refSetters: new Set(), checks: new Map() };
    for (let p: ts.Node | undefined = n; p; p = p.parent) {
      const s = scopes.get(p);
      if (!s) continue;
      s.setters.forEach((x) => out.setters.add(x));
      s.alive.forEach((x) => out.alive.add(x));
      s.dead.forEach((x) => out.dead.add(x));
      s.callbacks.forEach((x) => out.callbacks.add(x));
      s.refSetters.forEach((x) => out.refSetters.add(x));
      out.aborts ||= s.aborts;
      s.checks.forEach((v, k) => { if (!out.checks.has(k)) out.checks.set(k, v); });
    }
    return out;
  };

  // Truthy only while mounted, and truthy whenever gone. The two are each
  // other's negation, which is what lets `!` flip one into the other.
  const impliesAlive = (e: ts.Expression, s: Scope, depth = 0): boolean => {
    e = unparen(e);
    if (depth > 8) return false;
    const flag = flagText(e);
    if (flag && s.alive.has(flag)) return true;
    if (ts.isPrefixUnaryExpression(e) && e.operator === ts.SyntaxKind.ExclamationToken) return deadImpliesTruthy(e.operand, s, depth + 1);
    if (ts.isBinaryExpression(e)) {
      const op = e.operatorToken.kind;
      if (op === ts.SyntaxKind.AmpersandAmpersandToken) return impliesAlive(e.left, s, depth + 1) || impliesAlive(e.right, s, depth + 1);
      if (op === ts.SyntaxKind.BarBarToken) return impliesAlive(e.left, s, depth + 1) && impliesAlive(e.right, s, depth + 1);
    }
    if (ts.isCallExpression(e) && ts.isIdentifier(e.expression) && e.arguments.length === 0) {
      const body = s.checks.get(e.expression.text);
      if (body) return impliesAlive(body, s, depth + 1);
    }
    return false;
  };
  const deadImpliesTruthy = (e: ts.Expression, s: Scope, depth = 0): boolean => {
    e = unparen(e);
    if (depth > 8) return false;
    const flag = flagText(e);
    if (flag && s.dead.has(flag)) return true;
    if (s.aborts && ts.isPropertyAccessExpression(e) && e.name.text === "aborted") return true;
    if (ts.isPrefixUnaryExpression(e) && e.operator === ts.SyntaxKind.ExclamationToken) return impliesAlive(e.operand, s, depth + 1);
    if (ts.isBinaryExpression(e)) {
      const op = e.operatorToken.kind;
      if (op === ts.SyntaxKind.BarBarToken) return deadImpliesTruthy(e.left, s, depth + 1) || deadImpliesTruthy(e.right, s, depth + 1);
      if (op === ts.SyntaxKind.AmpersandAmpersandToken) return deadImpliesTruthy(e.left, s, depth + 1) && deadImpliesTruthy(e.right, s, depth + 1);
    }
    if (ts.isCallExpression(e) && ts.isIdentifier(e.expression) && e.arguments.length === 0) {
      const body = s.checks.get(e.expression.text);
      if (body) return deadImpliesTruthy(body, s, depth + 1);
    }
    return false;
  };

  /** Awaits that belong to `fn` itself, not to a function inside it, with
   *  the position each one hands control back at. */
  const resumePoints = (root: ts.Node, fn: Fn): { pos: number; at: ts.Node }[] => {
    const out: { pos: number; at: ts.Node }[] = [];
    const walk = (m: ts.Node) => {
      if (isFn(m) && m !== fn) return;
      if (ts.isAwaitExpression(m)) out.push({ pos: m.end, at: m });
      if (ts.isForOfStatement(m) && m.awaitModifier) out.push({ pos: m.statement.getStart(sf), at: m.statement });
      ts.forEachChild(m, walk);
    };
    walk(root);
    return out;
  };
  const isLoop = (n: ts.Node): n is ts.IterationStatement =>
    ts.isForStatement(n) || ts.isForInStatement(n) || ts.isForOfStatement(n) || ts.isWhileStatement(n) || ts.isDoStatement(n);

  /** Where the call stops being synchronous with whatever started it: the
   *  function that awaited, the position control came back at, and the
   *  await itself when it was one. */
  const origin = (node: ts.Node): Origin | undefined => {
    // The call's end, not its start: `setX(await read())` runs the setter
    // after the await in its own argument.
    let pos = node.end;
    let cur: ts.Node = node;
    for (;;) {
      const fn = enclosingFn(cur);
      if (!fn || !fn.body) return undefined;
      let from = isPromiseCallback(fn) ? fn.body.getStart(sf) : -1;
      let at: ts.Node | undefined;
      if (isAsync(fn)) {
        for (const r of resumePoints(fn.body, fn)) if (r.pos < pos && r.pos > from) { from = r.pos; at = r.at; }
        for (let p = cur.parent; p && p !== fn; p = p.parent) {
          if (isLoop(p) && resumePoints(p, fn).length > 0 && p.statement.getStart(sf) > from) { from = p.statement.getStart(sf); at = undefined; }
        }
      }
      if (from >= 0) return { fn, from, at, call: cur };
      if (isAsync(fn)) return undefined;
      pos = fn.getStart(sf);
      cur = fn;
    }
  };

  const contains = (outer: ts.Node, inner: ts.Node) => inner.pos >= outer.pos && inner.end <= outer.end;

  const isExitGuard = (st: ts.Statement, s: Scope) =>
    ts.isIfStatement(st) && !st.elseStatement && exits(st.thenStatement) && deadImpliesTruthy(st.expression, s);

  /** The other way a call can be covered: the await sits in a nested block
   *  (an `if`, a `try`) that checks before it finishes, so the only way out
   *  of that block to the call has passed the check. A `try` whose catch can
   *  fall through to the call doesn't count, since a rejected await goes
   *  round the check. */
  const checkedOnTheWayOut = (o: Origin, s: Scope): boolean => {
    if (!o.at) return false;
    let found = false;
    let child: ts.Node = o.at;
    let p: ts.Node | undefined = o.at.parent;
    for (; p && p !== o.fn && !contains(p, o.call); child = p, p = p.parent) {
      if (ts.isTryStatement(p) && child === p.tryBlock && found && p.catchClause && !exits(p.catchClause.block)) return false;
      if (!found && (ts.isBlock(p) || ts.isCaseClause(p) || ts.isDefaultClause(p))) {
        const i = p.statements.indexOf(child as ts.Statement);
        found = p.statements.slice(i + 1).some((st) => isExitGuard(st, s));
      }
    }
    // A call in the catch or the finally of the try the await is in is
    // reached without going through the try block's check.
    if (p && ts.isTryStatement(p) && child === p.tryBlock) return false;
    return found;
  };

  const exits = (s: ts.Statement): boolean => {
    if (ts.isReturnStatement(s) || ts.isThrowStatement(s) || ts.isContinueStatement(s) || ts.isBreakStatement(s)) return true;
    if (ts.isBlock(s)) return s.statements.length > 0 && exits(s.statements[s.statements.length - 1]);
    return false;
  };

  const guarded = (node: ts.Node, o: Origin, s: Scope): boolean => {
    let child: ts.Node = node;
    for (let p = node.parent; p && p !== o.fn; child = p, p = p.parent) {
      if (ts.isIfStatement(p) && p.expression.getStart(sf) >= o.from) {
        if (child === p.thenStatement && impliesAlive(p.expression, s)) return true;
        if (child === p.elseStatement && deadImpliesTruthy(p.expression, s)) return true;
      }
      if (ts.isConditionalExpression(p) && p.condition.getStart(sf) >= o.from) {
        if (child === p.whenTrue && impliesAlive(p.condition, s)) return true;
        if (child === p.whenFalse && deadImpliesTruthy(p.condition, s)) return true;
      }
      if (ts.isBinaryExpression(p) && child === p.right && p.left.getStart(sf) >= o.from) {
        const op = p.operatorToken.kind;
        if (op === ts.SyntaxKind.AmpersandAmpersandToken && impliesAlive(p.left, s)) return true;
        if (op === ts.SyntaxKind.BarBarToken && deadImpliesTruthy(p.left, s)) return true;
      }
      if (ts.isBlock(p) || ts.isCaseClause(p) || ts.isDefaultClause(p)) {
        for (const st of p.statements) {
          if (st === child) break;
          if (st.getStart(sf) >= o.from && isExitGuard(st, s)) return true;
        }
      }
    }
    return checkedOnTheWayOut(o, s);
  };

  // A helper that calls a setter (or another such helper) synchronously is a
  // setter too, as far as a caller after an await is concerned, unless it
  // checks for itself.
  const setsStateNow = (fn: Fn, s: Scope, self: string): boolean => {
    let found = false;
    // An async function only sets state synchronously up to its first await.
    const firstAwait = isAsync(fn) && fn.body ? Math.min(Infinity, ...resumePoints(fn.body, fn).map((r) => r.pos)) : Infinity;
    const walk = (m: ts.Node) => {
      if (found || (isFn(m) && m !== fn)) return;
      if (
        m.getStart(sf) < firstAwait && ts.isCallExpression(m) && ts.isIdentifier(m.expression) && m.expression.text !== self && s.setters.has(m.expression.text) &&
        !guarded(m, { fn, from: fn.body ? fn.body.getStart(sf) : 0, call: m }, s)
      ) found = true;
      ts.forEachChild(m, walk);
    };
    if (fn.body) walk(fn.body);
    return found;
  };
  const ownerOf = (sc: Scope): ts.Node => [...scopes.entries()].find(([, v]) => v === sc)![0];
  const props = new Set<string>();
  for (let changed = true; changed; ) {
    changed = false;
    for (const a of aliases) {
      if (a.scope.setters.has(a.name) || !visible(ownerOf(a.scope)).setters.has(a.target)) continue;
      a.scope.setters.add(a.name);
      changed = true;
    }
    for (const r of refs) {
      if (r.scope.refSetters.has(r.name) || !visible(ownerOf(r.scope)).setters.has(r.target)) continue;
      r.scope.refSetters.add(r.name);
      changed = true;
    }
    for (const h of propHelpers) {
      if (props.has(h.name) || !setsStateNow(h.fn, visible(h.fn), "")) continue;
      props.add(h.name);
      changed = true;
    }
    for (const h of helpers) {
      if (h.scope.setters.has(h.name) || !setsStateNow(h.fn, visible(h.fn), h.name)) continue;
      h.scope.setters.add(h.name);
      changed = true;
    }
  }

  /** `Component.handler`: the named functions around a node, outermost first. */
  const nameOf = (node: ts.Node): string => {
    const names: string[] = [];
    for (let p = node.parent; p; p = p.parent) {
      if (!isFn(p)) continue;
      let name: string | undefined;
      if ((ts.isFunctionDeclaration(p) || ts.isMethodDeclaration(p) || ts.isFunctionExpression(p)) && p.name) name = p.name.getText(sf);
      const up = p.parent;
      if (!name && ts.isVariableDeclaration(up) && ts.isIdentifier(up.name)) name = up.name.text;
      if (!name && ts.isPropertyAssignment(up)) name = up.name.getText(sf);
      if (!name && ts.isJsxExpression(up) && ts.isJsxAttribute(up.parent)) name = up.parent.name.getText(sf);
      if (!name && ts.isCallExpression(up)) {
        const callee = calleeName(up) ?? "";
        if (EFFECTS.has(callee)) name = "effect";
        else if ((callee === "useCallback" || callee === "useMemo") && ts.isVariableDeclaration(up.parent) && ts.isIdentifier(up.parent.name)) name = up.parent.name.text;
      }
      if (name) names.unshift(name);
    }
    return names.join(".") || "(module)";
  };

  const sites: Site[] = [];
  const report = (node: ts.Node, setter: string) => {
    const line = sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1;
    sites.push({ file, line, key: file.replace(/^\.\.\//, "") + " › " + nameOf(node) + " › " + setter });
  };
  const visit = (n: ts.Node) => {
    if (ts.isCallExpression(n)) {
      const s = visible(n);
      const e = n.expression;
      let name: string | undefined;
      if (ts.isIdentifier(e) && (s.setters.has(e.text) || s.callbacks.has(e.text))) name = e.text;
      // `source.done()`, a state-setting function some object carries.
      else if (ts.isPropertyAccessExpression(e) && (props.has(e.name.text) || shared.has(e.name.text)) && !PROMISE_CALLBACKS.has(e.name.text)) name = e.name.text;
      // `ref.current(...)` where the ref holds a setter, `props.onSaved(...)`.
      else if (ts.isPropertyAccessExpression(e) && e.name.text === "current" && ts.isIdentifier(e.expression) && s.refSetters.has(e.expression.text)) name = e.expression.text + ".current";
      else if (ts.isPropertyAccessExpression(e) && ts.isIdentifier(e.expression) && e.expression.text === "props" && /^(on|set)[A-Z]/.test(e.name.text)) name = "props." + e.name.text;
      if (name) {
        const o = origin(n);
        if (o && !guarded(n, o, s)) report(n, name);
      }
      if (ts.isPropertyAccessExpression(e) && PROMISE_CALLBACKS.has(e.name.text)) {
        for (const a of n.arguments) if (ts.isIdentifier(a) && (s.setters.has(a.text) || s.callbacks.has(a.text))) report(a, a.text);
      }
    }
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return { sites, props };
}

function findSites(file: string, text: string, shared: ReadonlySet<string> = new Set()): Site[] {
  return analyse(file, text, shared).sites;
}

function allSites(): Site[] {
  const shared = new Set<string>();
  for (const [file, text] of Object.entries(sources)) for (const p of analyse(file, text, shared).props) shared.add(p);
  return Object.entries(sources).flatMap(([file, text]) => findSites(file, text, shared));
}

describe("#1599 no state setter runs after an await without checking the component is still mounted", () => {
  it("every setter after an await is guarded, or allowed below with its reason", () => {
    const offenders = allSites()
      .filter((s) => !(s.key in ALLOWED) && !PENDING.has(s.key))
      .map((s) => s.file.replace(/^\.\.\//, "") + ":" + s.line + "  " + s.key);
    expect(offenders, "unguarded setters after an await:\n" + offenders.join("\n")).toEqual([]);
  });

  it("every allowed site still exists, so the list can't outlive what it excused", () => {
    const keys = new Set(allSites().map((s) => s.key));
    expect(Object.keys(ALLOWED).filter((k) => !keys.has(k))).toEqual([]);
  });

  it("every pending site is still waiting, so a fixed one comes off the list", () => {
    const keys = new Set(allSites().map((s) => s.key));
    expect([...PENDING].filter((k) => !keys.has(k))).toEqual([]);
  });

  it("every allowed site says why", () => {
    expect(Object.entries(ALLOWED).filter(([, why]) => why.trim().length < 20).map(([k]) => k)).toEqual([]);
  });
});

describe("#1599 the guard check itself", () => {
  const component = (body: string) =>
    [
      "import { useEffect, useState } from 'react';",
      "import { useMounted } from '@shared/hooks/useMounted';",
      "export function Card({ api }: { api: any }) {",
      "  const [x, setX] = useState(0);",
      "  const mountedRef = useMounted();",
      body,
      "  return null;",
      "}"
    ].join("\n");
  const flagged = (body: string) => findSites("x.tsx", component(body)).map((s) => s.key);

  it("flags a setter after an await with nothing checking", () => {
    expect(flagged("  const go = async () => { await api.a(); setX(1); };")).toEqual(["x.tsx › Card.go › setX"]);
  });

  it("flags a setter in a .then callback, and one handed straight to .then", () => {
    expect(flagged("  const go = () => { api.a().then((v: number) => setX(v)); };")).toEqual(["x.tsx › Card.go › setX"]);
    expect(flagged("  const go = () => { api.a().then(setX); };")).toEqual(["x.tsx › Card.go › setX"]);
  });

  it("counts an await inside the setter's own argument", () => {
    expect(flagged("  const go = async () => { setX(await api.a()); };")).toEqual(["x.tsx › Card.go › setX"]);
    expect(flagged("  const go = async () => { mountedRef.current && setX(await api.a()); };")).toEqual(["x.tsx › Card.go › setX"]);
  });

  it("leaves a setter before the first await alone", () => {
    expect(flagged("  const go = async () => { setX(1); await api.a(); };")).toEqual([]);
    expect(flagged("  const go = () => { setX(1); };")).toEqual([]);
  });

  it("takes an exit guard after the await, and refuses one before it", () => {
    expect(flagged("  const go = async () => { await api.a(); if (!mountedRef.current) return; setX(1); };")).toEqual([]);
    expect(flagged("  const go = async () => { if (!mountedRef.current) return; await api.a(); setX(1); };")).toEqual(["x.tsx › Card.go › setX"]);
  });

  it("checks the guard each time control comes back", () => {
    expect(flagged("  const go = async () => { await api.a(); if (!mountedRef.current) return; setX(1); await api.b(); setX(2); };")).toEqual([
      "x.tsx › Card.go › setX"
    ]);
  });

  it("takes a guarded call, && and ?: and refuses the wrong way round", () => {
    expect(flagged("  const go = async () => { await api.a(); if (mountedRef.current) setX(1); };")).toEqual([]);
    expect(flagged("  const go = async () => { await api.a(); mountedRef.current && setX(1); };")).toEqual([]);
    expect(flagged("  const go = () => api.a().then(() => mountedRef.current ? setX(1) : undefined);")).toEqual([]);
    expect(flagged("  const go = async () => { await api.a(); if (mountedRef.current) return; setX(1); };")).toEqual(["x.tsx › Card.go › setX"]);
    expect(flagged("  const go = async () => { await api.a(); if (!mountedRef.current) setX(1); };")).toEqual(["x.tsx › Card.go › setX"]);
  });

  it("takes an effect's own flag, read the right way round", () => {
    const live = "  useEffect(() => { let live = true; api.a().then((v: number) => { if (live) setX(v); }); return () => { live = false; }; }, []);";
    const cancelled = "  useEffect(() => { let cancelled = false; void (async () => { await api.a(); if (cancelled) return; setX(1); })(); return () => { cancelled = true; }; }, []);";
    const backwards = "  useEffect(() => { let cancelled = false; api.a().then(() => { if (cancelled) setX(1); }); return () => { cancelled = true; }; }, []);";
    expect(flagged(live)).toEqual([]);
    expect(flagged(cancelled)).toEqual([]);
    expect(flagged(backwards)).toEqual(["x.tsx › Card.effect › setX"]);
  });

  it("takes an aborted signal only when a cleanup aborts it", () => {
    const aborted = "  useEffect(() => { const c = new AbortController(); api.a().then(() => { if (c.signal.aborted) return; setX(1); }); return () => c.abort(); }, []);";
    const never = "  useEffect(() => { const c = new AbortController(); api.a().then(() => { if (c.signal.aborted) return; setX(1); }); }, []);";
    expect(flagged(aborted)).toEqual([]);
    expect(flagged(never)).toEqual(["x.tsx › Card.effect › setX"]);
  });

  it("doesn't take a newest-answer counter on its own, but takes a check that includes the mounted ref", () => {
    expect(flagged("  const go = async () => { const mine = 1; await api.a(); if (mine !== 1) return; setX(1); };")).toEqual(["x.tsx › Card.go › setX"]);
    expect(flagged("  const go = async () => { const current = () => mountedRef.current && x === 0; await api.a(); if (!current()) return; setX(1); };")).toEqual([]);
  });

  it("follows the call into a try, its catch and its finally", () => {
    const body =
      "  const go = async () => { try { await api.a(); if (!mountedRef.current) return; setX(1); } catch { setX(2); } finally { if (mountedRef.current) setX(3); } };";
    expect(findSites("x.tsx", component(body)).map((s) => s.line)).toEqual([6]);
  });

  it("takes a check that ends the block the await is in", () => {
    const inIf = "  const go = async (f: boolean) => { if (f) { await api.a(); if (!mountedRef.current) return; setX(1); } setX(2); };";
    expect(flagged(inIf)).toEqual([]);
    const inTry = "  const go = async () => { try { await api.a(); if (!mountedRef.current) return; } catch { return; } setX(1); };";
    expect(flagged(inTry)).toEqual([]);
    const fallsThrough = "  const go = async () => { try { await api.a(); if (!mountedRef.current) return; } catch { setX(0); } setX(1); };";
    expect(flagged(fallsThrough)).toEqual(["x.tsx › Card.go › setX", "x.tsx › Card.go › setX"]);
    const checkFirst = "  const go = async (f: boolean) => { if (f) { if (!mountedRef.current) return; await api.a(); } setX(2); };";
    expect(flagged(checkFirst)).toEqual(["x.tsx › Card.go › setX"]);
  });

  it("counts a setter at the top of a loop that awaits", () => {
    expect(flagged("  const go = async () => { for (const id of [1, 2]) { setX(id); await api.a(id); } };")).toEqual(["x.tsx › Card.go › setX"]);
  });

  it("counts a callback made after an await, unless the check comes first", () => {
    expect(flagged("  const go = async () => { await api.a(); setTimeout(() => setX(0), 10); };")).toEqual(["x.tsx › Card.go › setX"]);
    expect(flagged("  const go = async () => { await api.a(); if (!mountedRef.current) return; [1].forEach((v) => setX(v)); };")).toEqual([]);
  });

  it("counts a plain helper that sets state, called after an await", () => {
    const body = "  const forget = () => { setX(0); };\n  const go = async () => { await api.a(); forget(); };";
    expect(flagged(body)).toEqual(["x.tsx › Card.go › forget"]);
    const deeper = "  const reset = useCallback(() => setX(0), []);\n  function both() { reset(); }\n  const go = async () => { await api.a(); both(); };";
    expect(flagged(deeper)).toEqual(["x.tsx › Card.go › both"]);
    const later = "  const soon = () => { setTimeout(() => setX(0), 10); };\n  const go = async () => { await api.a(); soon(); };";
    expect(flagged(later)).toEqual([]);
    const checks = "  const done = () => { if (mountedRef.current) setX(0); };\n  const go = async () => { await api.a(); done(); };";
    expect(flagged(checks)).toEqual([]);
  });

  it("counts an async helper that sets state before its own first await", () => {
    const body = "  const reset = async () => { setX(0); await api.b(); };\n  const go = async () => { await api.a(); await reset(); };";
    expect(flagged(body)).toEqual(["x.tsx › Card.go › reset"]);
    const later = "  const reset = async () => { await api.b(); if (!mountedRef.current) return; setX(0); };\n  const go = async () => { await api.a(); await reset(); };";
    expect(flagged(later)).toEqual([]);
    const first = "  const reset = async () => { setX(0); await api.b(); };\n  const go = async () => { await reset(); };";
    expect(flagged(first)).toEqual([]);
  });

  it("counts a function an object carries, called as obj.name() after an await", () => {
    const body = "  const source = { done: () => { setX(0); } };\n  const go = async () => { await api.a(); source.done(); };";
    expect(flagged(body)).toEqual(["x.tsx › Card.go › done"]);
    const guarded = "  const source = { done: () => { if (mountedRef.current) setX(0); } };\n  const go = async () => { await api.a(); source.done(); };";
    expect(flagged(guarded)).toEqual([]);
  });

  it("counts an aliased setter, a ref that holds one, and a setter out of an array-destructured hook", () => {
    expect(flagged("  const put = setX;\n  const go = async () => { await api.a(); put(1); };")).toEqual(["x.tsx › Card.go › put"]);
    expect(flagged("  const ref = useRef(setX);\n  const go = async () => { await api.a(); ref.current(1); };")).toEqual(["x.tsx › Card.go › ref.current"]);
    expect(flagged("  const last = useRef(null);\n  last.current = setX;\n  const go = async () => { await api.a(); last.current(1); };")).toEqual(["x.tsx › Card.go › last.current"]);
    expect(flagged("  const [y, setY] = useCustom();\n  const go = async () => { await api.a(); setY(1); };")).toEqual(["x.tsx › Card.go › setY"]);
  });

  it("counts a callback or setter a parent handed down, called after an await", () => {
    const props = (call: string) =>
      "export function Row({ onSaved, setItems }: { onSaved(v: number): void; setItems(v: number): void }) {\n" +
      "  const mountedRef = useMounted();\n" + call + "\n  return null;\n}";
    const run = (call: string) => findSites("y.tsx", props(call)).map((s) => s.key);
    expect(run("  const go = async () => { const v = await api.a(); onSaved(v); };")).toEqual(["y.tsx › Row.go › onSaved"]);
    expect(run("  const go = async () => { const v = await api.a(); setItems(v); };")).toEqual(["y.tsx › Row.go › setItems"]);
    expect(run("  const go = () => { api.a().then(onSaved); };")).toEqual(["y.tsx › Row.go › onSaved"]);
    expect(run("  const go = async () => { const v = await api.a(); if (!mountedRef.current) return; onSaved(v); };")).toEqual([]);
    expect(run("  const go = async () => { onSaved(1); await api.a(); };")).toEqual([]);
  });

  it("leaves a callback parameter of a plain function alone", () => {
    const body = "export async function pump(onLine: (l: string) => void) { await read(); onLine('x'); }";
    expect(findSites("z.ts", body)).toEqual([]);
  });

  it("sees set* names destructured from another hook", () => {
    const body = "  const { setOpen } = useDock();\n  const go = async () => { await api.a(); setOpen(false); };";
    expect(flagged(body)).toEqual(["x.tsx › Card.go › setOpen"]);
  });
});
