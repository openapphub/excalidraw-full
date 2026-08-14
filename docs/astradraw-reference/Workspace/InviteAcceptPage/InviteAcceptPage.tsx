import React, { useEffect, useState, useCallback } from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import { useAuth } from "../../../auth";
import { joinViaInviteLink } from "../../../auth/workspaceApi";

import { LoginDialog } from "../LoginDialog";

import styles from "./InviteAcceptPage.module.scss";

import type { Workspace } from "../../../auth/workspaceApi";

interface InviteAcceptPageProps {
  inviteCode: string;
  onSuccess: (workspace: Workspace) => void;
  onCancel: () => void;
}

export const InviteAcceptPage: React.FC<InviteAcceptPageProps> = ({
  inviteCode,
  onSuccess,
  onCancel,
}) => {
  const { isAuthenticated, isLoading: authLoading } = useAuth();
  const [isJoining, setIsJoining] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showLoginDialog, setShowLoginDialog] = useState(false);
  const [hasAttemptedJoin, setHasAttemptedJoin] = useState(false);

  const handleJoinWorkspace = useCallback(async () => {
    if (isJoining || hasAttemptedJoin) {
      return;
    }

    setIsJoining(true);
    setError(null);
    setHasAttemptedJoin(true);

    try {
      const workspace = await joinViaInviteLink(inviteCode);
      // Clear the invite URL from browser history
      window.history.replaceState({}, document.title, "/");
      onSuccess(workspace);
    } catch (err: any) {
      const message = err.message || t("workspace.invite.error");
      // Map specific error messages to translations
      if (message.includes("Invalid invite link")) {
        setError(t("workspace.invite.invalidLink"));
      } else if (message.includes("expired") || message.includes("max uses")) {
        setError(t("workspace.invite.invalidLink"));
      } else if (message.includes("already")) {
        setError(t("workspace.invite.alreadyMember"));
      } else {
        setError(message);
      }
      setIsJoining(false);
    }
  }, [inviteCode, onSuccess, isJoining, hasAttemptedJoin]);

  // Auto-join when authenticated
  useEffect(() => {
    if (!authLoading && isAuthenticated && !hasAttemptedJoin) {
      handleJoinWorkspace();
    }
  }, [authLoading, isAuthenticated, hasAttemptedJoin, handleJoinWorkspace]);

  // Show login dialog for unauthenticated users
  useEffect(() => {
    if (!authLoading && !isAuthenticated) {
      setShowLoginDialog(true);
    }
  }, [authLoading, isAuthenticated]);

  const handleLoginSuccess = useCallback(() => {
    setShowLoginDialog(false);
    // Reset the join attempt flag so we can try again after login
    setHasAttemptedJoin(false);
  }, []);

  const handleLoginClose = useCallback(() => {
    setShowLoginDialog(false);
    onCancel();
  }, [onCancel]);

  const handleRetry = useCallback(() => {
    setHasAttemptedJoin(false);
    setError(null);
  }, []);

  const handleGoHome = useCallback(() => {
    window.history.replaceState({}, document.title, "/");
    onCancel();
  }, [onCancel]);

  // Show loading while checking auth
  if (authLoading) {
    return (
      <div className={styles.page}>
        <div className={styles.card}>
          <div className={styles.loading}>
            <div className={styles.spinner} />
            <p>{t("workspace.invite.joiningWorkspace")}</p>
          </div>
        </div>
      </div>
    );
  }

  // Show login dialog for unauthenticated users
  if (!isAuthenticated) {
    return (
      <div className={styles.page}>
        <div className={styles.card}>
          <div className={styles.icon}>
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
              <circle cx="9" cy="7" r="4" />
              <path d="M22 21v-2a4 4 0 0 0-3-3.87" />
              <path d="M16 3.13a4 4 0 0 1 0 7.75" />
            </svg>
          </div>
          <h1>{t("workspace.invite.loginToJoin")}</h1>
          <p className={styles.description}>
            {t("workspace.invite.loginDescription")}
          </p>
          <button
            className={`${styles.button} ${styles.buttonPrimary}`}
            onClick={() => setShowLoginDialog(true)}
          >
            {t("workspace.login")}
          </button>
          <button
            className={`${styles.button} ${styles.buttonSecondary}`}
            onClick={handleGoHome}
          >
            {t("workspace.invite.cancel")}
          </button>
        </div>

        <LoginDialog
          isOpen={showLoginDialog}
          onClose={handleLoginClose}
          onSuccess={handleLoginSuccess}
        />
      </div>
    );
  }

  // Show error state
  if (error) {
    return (
      <div className={styles.page}>
        <div className={styles.card}>
          <div className={`${styles.icon} ${styles.iconError}`}>
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <circle cx="12" cy="12" r="10" />
              <line x1="12" y1="8" x2="12" y2="12" />
              <line x1="12" y1="16" x2="12.01" y2="16" />
            </svg>
          </div>
          <h1>{t("workspace.invite.errorTitle")}</h1>
          <p className={styles.error}>{error}</p>
          <div className={styles.actions}>
            <button
              className={`${styles.button} ${styles.buttonPrimary}`}
              onClick={handleRetry}
            >
              {t("workspace.invite.tryAgain")}
            </button>
            <button
              className={`${styles.button} ${styles.buttonSecondary}`}
              onClick={handleGoHome}
            >
              {t("workspace.invite.goHome")}
            </button>
          </div>
        </div>
      </div>
    );
  }

  // Show joining state
  return (
    <div className={styles.page}>
      <div className={styles.card}>
        <div className={styles.loading}>
          <div className={styles.spinner} />
          <p>{t("workspace.invite.joiningWorkspace")}</p>
        </div>
      </div>
    </div>
  );
};

export default InviteAcceptPage;
