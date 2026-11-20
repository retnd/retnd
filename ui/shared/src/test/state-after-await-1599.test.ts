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
const ALLOWED: Record<string, string> = {};

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
  "auth/EnrollmentPage.tsx › EnrollmentPage.submit › setBusy",
  "auth/EnrollmentPage.tsx › EnrollmentPage.submit › setFailure",
  "auth/ForgotPasswordPage.tsx › ForgotPasswordPage.submit › setAsked",
  "auth/ForgotPasswordPage.tsx › ForgotPasswordPage.submit › setBusy",
  "auth/ForgotPasswordPage.tsx › ForgotPasswordPage.submit › setFailure",
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
  "components/RemoveBackupSetDialog.tsx › RemoveBackupSetDialog.onConfirm › setError",
  "components/RemoveBackupSetDialog.tsx › RemoveBackupSetDialog.onConfirm › setRemoving",
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
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.acknowledge › setAcknowledging",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.acknowledge › setBusy",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.acknowledge › setFailure",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.acknowledge › setReason",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.resume › setBusy",
  "components/WorkflowRecoveryBanner.tsx › WorkflowRecoveryBanner.resume › setFailure",
  "encryption/BundleDialog.tsx › BundleDialog.download › forget",
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
  "encryption/SuiteControls.tsx › SuiteChangeFlow.onConfirm › setKeyId",
  "encryption/SuiteControls.tsx › SuiteChangeFlow.onConfirm › setStep",
  "encryption/SuiteControls.tsx › SuiteChangeFlow.run › setBusy",
  "encryption/SuiteControls.tsx › SuiteChangeFlow.run › setFailure",
  "encryption/VerifyRecoverableDialog.tsx › VerifyRecoverableDialog.start › setBusy",
  "encryption/VerifyRecoverableDialog.tsx › VerifyRecoverableDialog.start › setOpId",
  "encryption/VerifyRecoverableDialog.tsx › VerifyRecoverableDialog.start › setRefusal",
  "observability/AlertDeliveryControls.tsx › AlertDeliveryControls.save › setBusy",
  "observability/AlertDeliveryControls.tsx › AlertDeliveryControls.save › setError",
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
  "pages/ActivityPage.tsx › ActivityPage.loadOlder › setLoadingOlder",
  "pages/ActivityPage.tsx › ActivityPage.loadOlder › setOlder",
  "pages/ActivityPage.tsx › ActivityPage.loadOlder › setOlderFailure",
  "pages/ActivityStrip.tsx › ActivityToolbar.copy › setCopied",
  "pages/BackupDetailPage.tsx › BackupDetailPage.retryIngestion › setRecovery",
  "pages/BackupDetailPage.tsx › BackupDetailPage.retryIngestion › setRetrying",
  "pages/BackupSetConfigurationCard.tsx › IncrementalConfiguration.save › setEditing",
  "pages/BackupSetConfigurationCard.tsx › IncrementalConfiguration.save › setSaveError",
  "pages/BackupSetConfigurationCard.tsx › IncrementalConfiguration.save › setSaving",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.confirmRefusal › leaveEditMode",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.enterEditMode › setBaseline",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.enterEditMode › setDraft",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.enterEditMode › setEditing",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.enterEditMode › setEnterError",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.enterEditMode › setFieldErrors",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.enterEditMode › setRefusal",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.enterEditMode › setSnapshot",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.enterEditMode › setStale",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.enterEditMode › setStopped",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.onEditPressed › setEnterError",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.onEditPressed › setWarnAbout",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.runConnectionTest › setSourceWritable",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.runConnectionTest › setTesting",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveAllAndExit › leaveEditMode",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveAllAndExit › setPartialSave",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveFields › setBaseline",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveFields › setDraft",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveFields › setFieldErrors",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveFields › setRefusal",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveFields › setSavedThisSession",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveFields › setSavingFields",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveFields › setSnapshot",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveHooks › setHookDraft",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveHooks › setHookErrors",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveHooks › setSavedThisSession",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.saveHooks › setSavingHooks",
  "pages/BackupSetDetailPage.tsx › BackupSetDetailPage.toggleReadOnly › setSourceWritable",
  "pages/BackupSetRetentionCard.tsx › RetentionPanel.apply › setBusy",
  "pages/BackupSetRetentionCard.tsx › RetentionPanel.apply › setError",
  "pages/BackupSetRetentionCard.tsx › RetentionPanel.apply › setR",
  "pages/BackupSetsPage.tsx › BackupSetsPage.toggleEnabled › publishBusy",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.handleSave › setRepointRefusal",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.handleSave › setRunNotStarted",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.handleSave › setSaveError",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.handleSave › setSaving",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.onClick › setImportedFingerprint",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.onClick › setImportedKeyId",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.onClick › setImportError",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.onClick › setImporting",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.onClick › setImportPasted",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.runConnectionTest › setConnectionError",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.runConnectionTest › setConnectionResult",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.runConnectionTest › setConnectionTestedFor",
  "pages/BackupSetWizardPage.tsx › BackupSetWizardPage.runConnectionTest › setTesting",
  "pages/BackupSetWorkflowCard.tsx › ExecConnectionPicker.onChange › setFailure",
  "pages/BackupSetWorkflowCard.tsx › ExecConnectionPicker.onChange › setSaving",
  "pages/BackupSetWorkflowCard.tsx › SetWorkflowCard.validate › setValidatedAt",
  "pages/BackupSetWorkflowCard.tsx › SetWorkflowCard.validate › setValidating",
  "pages/BackupSetWorkflowCard.tsx › SetWorkflowCard.validate › setValidation",
  "pages/BackupSetWorkflowCard.tsx › SetWorkflowCard.validate › setValidationError",
  "pages/CapacityCard.tsx › CapacityEditor.onSave › setBaseline",
  "pages/CapacityCard.tsx › CapacityEditor.onSave › setBusy",
  "pages/CapacityCard.tsx › CapacityEditor.onSave › setCap",
  "pages/CapacityCard.tsx › CapacityEditor.onSave › setCritical",
  "pages/CapacityCard.tsx › CapacityEditor.onSave › setSaved",
  "pages/CapacityCard.tsx › CapacityEditor.onSave › setSaveError",
  "pages/CapacityCard.tsx › CapacityEditor.onSave › setWarning",
  "pages/CatalogRecoveryPage.tsx › CatalogRecoveryPage.onConfirm › setConfirming",
  "pages/CatalogRecoveryPage.tsx › CatalogRecoveryPage.onConfirm › setFailure",
  "pages/CatalogRecoveryPage.tsx › CatalogRecoveryPage.scan › setFailure",
  "pages/CatalogRecoveryPage.tsx › CatalogRecoveryPage.scan › setPreview",
  "pages/CatalogRecoveryPage.tsx › CatalogRecoveryPage.scan › setScanning",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.runProbe › setBusy",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.runProbe › setCredential",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.runProbe › setCredentialConfigured",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.runProbe › setCredentialsId",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.runProbe › setElapsedMs",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.runProbe › setFailure",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.runProbe › setProvenFor",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.runProbe › setReport",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.save › setBusy",
  "pages/DestinationConfigureWizard.tsx › DestinationConfigureWizard.save › setFailure",
  "pages/QuarantinePage.tsx › QuarantinePage.reinstate › setConfirming",
  "pages/QuarantinePage.tsx › QuarantinePage.reinstate › setOutcome",
  "pages/QuarantinePage.tsx › QuarantinePage.reinstate › setRunning",
  "pages/QuarantinePage.tsx › QuarantinePage.retryIngestion › setOutcome",
  "pages/QuarantinePage.tsx › QuarantinePage.revalidate › setOutcome",
  "pages/RepositoryDomainNewPage.tsx › RepositoryDomainNewPage.create › setRefusal",
  "pages/RepositoryDomainNewPage.tsx › RepositoryDomainNewPage.create › setSaving",
  "pages/retentionChain.tsx › MediumPreflightRow.run › setBusy",
  "pages/retentionChain.tsx › MediumPreflightRow.run › setError",
  "pages/retentionChain.tsx › MediumPreflightRow.run › setReport",
  "pages/retentionChain.tsx › TierDestinationActions.test › setBusy",
  "pages/retentionChain.tsx › TierDestinationActions.test › setError",
  "pages/retentionChain.tsx › TierDestinationActions.test › setReport",
  "pages/RetentionPolicyCard.tsx › RetentionPolicyEditor.submit › setAcknowledged",
  "pages/RetentionPolicyCard.tsx › RetentionPolicyEditor.submit › setBaseline",
  "pages/RetentionPolicyCard.tsx › RetentionPolicyEditor.submit › setBusy",
  "pages/RetentionPolicyCard.tsx › RetentionPolicyEditor.submit › setProtect",
  "pages/RetentionPolicyCard.tsx › RetentionPolicyEditor.submit › setSaved",
  "pages/RetentionPolicyCard.tsx › RetentionPolicyEditor.submit › setSaveError",
  "pages/RetentionPolicyCard.tsx › RetentionPolicyEditor.submit › setTiers",
  "pages/RetentionPolicyCard.tsx › RetentionPolicyEditor.submit › setTimezone",
  "pages/RetentionPolicyCard.tsx › RetentionPolicyEditor.submit › setWeekStartsOn",
  "pages/RetentionPreviewDialog.tsx › RetentionPreviewDialog.handleApply › setApplyError",
  "pages/RetentionPreviewDialog.tsx › RetentionPreviewDialog.handleApply › setConfirming",
  "pages/RetentionPreviewDialog.tsx › RetentionPreviewDialog.handleApply › setGateNotice",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.save › setBusy",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.save › setFailure",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.toVerify › setAccessKeyId",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.toVerify › setBusy",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.toVerify › setCredentialsId",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.toVerify › setFailure",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.toVerify › setReport",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.toVerify › setSecretAccessKey",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.toVerify › setStep",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.verifyAgain › setBusy",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.verifyAgain › setFailure",
  "pages/S3DestinationWizard.tsx › S3DestinationWizard.verifyAgain › setReport",
  "pages/ServiceBehaviourCard.tsx › ServiceBehaviourEditor.onSave › setBaselineSeconds",
  "pages/ServiceBehaviourCard.tsx › ServiceBehaviourEditor.onSave › setBusy",
  "pages/ServiceBehaviourCard.tsx › ServiceBehaviourEditor.onSave › setMinutes",
  "pages/ServiceBehaviourCard.tsx › ServiceBehaviourEditor.onSave › setSaved",
  "pages/ServiceBehaviourCard.tsx › ServiceBehaviourEditor.onSave › setSaveError",
  "pages/SettingsPage.tsx › ChangePasswordCard.submit › setBusy",
  "pages/SettingsPage.tsx › ChangePasswordCard.submit › setConfirm",
  "pages/SettingsPage.tsx › ChangePasswordCard.submit › setCurrent",
  "pages/SettingsPage.tsx › ChangePasswordCard.submit › setFailure",
  "pages/SettingsPage.tsx › ChangePasswordCard.submit › setNext",
  "pages/SettingsPage.tsx › ChangePasswordCard.submit › setSuccess",
  "pages/SettingsPage.tsx › RecoveryEditor.save › setCurrent",
  "pages/SettingsPage.tsx › RecoveryEditor.save › setEmail",
  "pages/SettingsPage.tsx › RecoveryEditor.save › setFailure",
  "pages/SettingsPage.tsx › RecoveryEditor.save › setPassword",
  "pages/SettingsPage.tsx › RecoveryEditor.save › setSaved",
  "pages/SettingsPage.tsx › RecoveryEditor.save › setSaving",
  "pages/SettingsPage.tsx › RecoveryEditor.save › setSmtp",
  "pages/SettingsPage.tsx › RecoveryEditor.sendTest › setFailure",
  "pages/SettingsPage.tsx › RecoveryEditor.sendTest › setTested",
  "pages/SettingsPage.tsx › RecoveryEditor.sendTest › setTesting",
  "pages/SnapshotRestorePage.tsx › SnapshotRestorePage.onFinish › setStepUpError",
  "pages/StorageDestinationsCard.tsx › DestinationRow.makeDefault › setBusy",
  "pages/StorageDestinationsCard.tsx › DestinationRow.makeDefault › setFailure",
  "pages/StorageDestinationsCard.tsx › DestinationRow.remove › setBusy",
  "pages/StorageDestinationsCard.tsx › DestinationRow.remove › setFailure",
  "pages/StorageDestinationsCard.tsx › DestinationRow.remove › setUsage",
  "pages/StorageDestinationsCard.tsx › DestinationRow.testConnection › setBusy",
  "pages/StorageDestinationsCard.tsx › DestinationRow.testConnection › setFailure",
  "pages/StorageDestinationsCard.tsx › DestinationRow.testConnection › setReport",
  "pages/StorageDestinationsCard.tsx › DestinationRow.testConnection › setUsage",
  "pages/useSetWorkflow.ts › useSetWorkflow.write › setRefusal",
  "pages/WorkflowSettingsCard.tsx › WorkflowSettingsCard.save › setFailure",
  "pages/WorkflowSettingsCard.tsx › WorkflowSettingsCard.save › setRefusal",
  "pages/WorkflowSettingsCard.tsx › WorkflowSettingsCard.save › setSaving",
]);

interface Site {
  file: string;
  line: number;
  key: string;
}

type Fn = ts.FunctionDeclaration | ts.FunctionExpression | ts.ArrowFunction | ts.MethodDeclaration;

interface Scope {
  setters: Set<string>;
  /** Expressions that are true while the component is there. */
  alive: Set<string>;
  /** Expressions that are true once it has gone. */
  dead: Set<string>;
  aborts: boolean;
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

function findSites(file: string, text: string): Site[] {
  const kind = file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
  const sf = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true, kind);
  const scopes = new Map<ts.Node, Scope>();
  const scopeOf = (n: ts.Node): Scope => {
    const owner = enclosingFn(n) ?? sf;
    let s = scopes.get(owner);
    if (!s) scopes.set(owner, (s = { setters: new Set(), alive: new Set(), dead: new Set(), aborts: false, checks: new Map() }));
    return s;
  };

  // Plain (not async) functions a component declares, which may set state
  // themselves: `const forget = () => { ...; setHasPassword(false); }`.
  const helpers: { scope: Scope; name: string; fn: Fn }[] = [];
  const helper = (n: ts.Node, name: string, fn: ts.Node) => {
    if (isFn(fn) && !isAsync(fn) && enclosingFn(n)) helpers.push({ scope: scopeOf(n), name, fn });
  };

  // First pass: what each function declares.
  const declare = (n: ts.Node) => {
    if (ts.isFunctionDeclaration(n) && n.name) helper(n, n.name.text, n);
    if (ts.isVariableDeclaration(n) && n.initializer && ts.isIdentifier(n.name)) {
      const init = unparen(n.initializer);
      if (ts.isCallExpression(init) && calleeName(init) === "useCallback" && init.arguments[0]) helper(n, n.name.text, init.arguments[0]);
      else helper(n, n.name.text, init);
    }
    if (ts.isVariableDeclaration(n) && n.initializer) {
      const init = unparen(n.initializer);
      if (ts.isCallExpression(init)) {
        const callee = calleeName(init);
        if (ts.isArrayBindingPattern(n.name) && (callee === "useState" || callee === "useReducer")) {
          const second = n.name.elements[1];
          if (second && ts.isBindingElement(second) && ts.isIdentifier(second.name)) scopeOf(n).setters.add(second.name.text);
        } else if (ts.isObjectBindingPattern(n.name) && callee && /^use[A-Z]/.test(callee)) {
          for (const el of n.name.elements) {
            if (ts.isIdentifier(el.name) && /^set[A-Z]/.test(el.name.text)) scopeOf(n).setters.add(el.name.text);
          }
        } else if (ts.isIdentifier(n.name) && callee === "useMounted") {
          scopeOf(n).alive.add(n.name.text + ".current");
        }
      } else if (ts.isIdentifier(n.name) && ts.isArrowFunction(init) && init.parameters.length === 0 && !ts.isBlock(init.body)) {
        scopeOf(n).checks.set(n.name.text, init.body);
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
    const out: Scope = { setters: new Set(), alive: new Set(), dead: new Set(), aborts: false, checks: new Map() };
    for (let p: ts.Node | undefined = n; p; p = p.parent) {
      const s = scopes.get(p);
      if (!s) continue;
      s.setters.forEach((x) => out.setters.add(x));
      s.alive.forEach((x) => out.alive.add(x));
      s.dead.forEach((x) => out.dead.add(x));
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
  const resumePoints = (root: ts.Node, fn: Fn): number[] => {
    const out: number[] = [];
    const walk = (m: ts.Node) => {
      if (isFn(m) && m !== fn) return;
      if (ts.isAwaitExpression(m)) out.push(m.end);
      if (ts.isForOfStatement(m) && m.awaitModifier) out.push(m.statement.getStart(sf));
      ts.forEachChild(m, walk);
    };
    walk(root);
    return out;
  };
  const isLoop = (n: ts.Node): n is ts.IterationStatement =>
    ts.isForStatement(n) || ts.isForInStatement(n) || ts.isForOfStatement(n) || ts.isWhileStatement(n) || ts.isDoStatement(n);

  /** Where the call stops being synchronous with whatever started it: the
   *  function that awaited and the position control came back at. */
  const origin = (node: ts.Node): { fn: Fn; from: number } | undefined => {
    // The call's end, not its start: `setX(await read())` runs the setter
    // after the await in its own argument.
    let pos = node.end;
    let cur: ts.Node = node;
    for (;;) {
      const fn = enclosingFn(cur);
      if (!fn || !fn.body) return undefined;
      let from = isPromiseCallback(fn) ? fn.body.getStart(sf) : -1;
      if (isAsync(fn)) {
        for (const r of resumePoints(fn.body, fn)) if (r <= pos) from = Math.max(from, r);
        for (let p = cur.parent; p && p !== fn; p = p.parent) {
          if (isLoop(p) && resumePoints(p, fn).length > 0) from = Math.max(from, p.statement.getStart(sf));
        }
      }
      if (from >= 0) return { fn, from };
      if (isAsync(fn)) return undefined;
      pos = fn.getStart(sf);
      cur = fn;
    }
  };

  const exits = (s: ts.Statement): boolean => {
    if (ts.isReturnStatement(s) || ts.isThrowStatement(s) || ts.isContinueStatement(s) || ts.isBreakStatement(s)) return true;
    if (ts.isBlock(s)) return s.statements.length > 0 && exits(s.statements[s.statements.length - 1]);
    return false;
  };

  const guarded = (node: ts.Node, o: { fn: Fn; from: number }, s: Scope): boolean => {
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
          if (st.getStart(sf) >= o.from && ts.isIfStatement(st) && !st.elseStatement && exits(st.thenStatement) && deadImpliesTruthy(st.expression, s)) return true;
        }
      }
    }
    return false;
  };

  // A helper that calls a setter (or another such helper) synchronously is a
  // setter too, as far as a caller after an await is concerned, unless it
  // checks for itself.
  const setsStateNow = (fn: Fn, s: Scope, self: string): boolean => {
    let found = false;
    const walk = (m: ts.Node) => {
      if (found || (isFn(m) && m !== fn)) return;
      if (
        ts.isCallExpression(m) && ts.isIdentifier(m.expression) && m.expression.text !== self && s.setters.has(m.expression.text) &&
        !guarded(m, { fn, from: fn.body ? fn.body.getStart(sf) : 0 }, s)
      ) found = true;
      ts.forEachChild(m, walk);
    };
    if (fn.body) walk(fn.body);
    return found;
  };
  for (let changed = true; changed; ) {
    changed = false;
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
      if (ts.isIdentifier(n.expression) && s.setters.has(n.expression.text)) {
        const o = origin(n);
        if (o && !guarded(n, o, s)) report(n, n.expression.text);
      }
      if (ts.isPropertyAccessExpression(n.expression) && PROMISE_CALLBACKS.has(n.expression.name.text)) {
        for (const a of n.arguments) if (ts.isIdentifier(a) && s.setters.has(a.text)) report(a, a.text);
      }
    }
    ts.forEachChild(n, visit);
  };
  visit(sf);
  return sites;
}

function allSites(): Site[] {
  return Object.entries(sources).flatMap(([file, text]) => findSites(file, text));
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

  it("sees set* names destructured from another hook", () => {
    const body = "  const { setOpen } = useDock();\n  const go = async () => { await api.a(); setOpen(false); };";
    expect(flagged(body)).toEqual(["x.tsx › Card.go › setOpen"]);
  });
});
