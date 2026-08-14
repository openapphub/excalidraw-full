import React, { useState, useId } from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import { useAuth } from "../../../auth";

import styles from "./LoginDialog.module.scss";

// Stop keyboard events from propagating to Excalidraw canvas
const stopPropagation = (e: React.KeyboardEvent) => {
  e.stopPropagation();
};

type DialogMode = "signin" | "signup";

interface LoginDialogProps {
  isOpen: boolean;
  onClose: () => void;
  onSuccess: () => void;
}

export const LoginDialog: React.FC<LoginDialogProps> = ({
  isOpen,
  onClose,
  onSuccess,
}) => {
  const {
    loginLocal,
    login,
    register,
    oidcConfigured,
    localAuthEnabled,
    registrationEnabled,
  } = useAuth();
  const formId = useId();
  const [mode, setMode] = useState<DialogMode>("signin");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);

  const resetForm = () => {
    setEmail("");
    setPassword("");
    setConfirmPassword("");
    setName("");
    setError(null);
  };

  const handleModeSwitch = (newMode: DialogMode) => {
    setMode(newMode);
    setError(null);
  };

  const handleLocalLogin = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setIsLoading(true);

    try {
      const success = await loginLocal(email, password);
      if (success) {
        onSuccess();
        onClose();
        resetForm();
      } else {
        setError(t("workspace.loginError"));
      }
    } catch (err) {
      setError(t("workspace.loginError"));
    } finally {
      setIsLoading(false);
    }
  };

  const handleRegister = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    // Validate password match
    if (password !== confirmPassword) {
      setError(t("workspace.passwordMismatch"));
      return;
    }

    // Validate password length
    if (password.length < 6) {
      setError(t("workspace.passwordTooShort"));
      return;
    }

    // Validate email format
    const emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    if (!emailRegex.test(email)) {
      setError(t("workspace.invalidEmail"));
      return;
    }

    setIsLoading(true);

    try {
      const success = await register(email, password, name || undefined);
      if (success) {
        onSuccess();
        onClose();
        resetForm();
      } else {
        setError(t("workspace.registrationError"));
      }
    } catch (err: any) {
      setError(err.message || t("workspace.registrationError"));
    } finally {
      setIsLoading(false);
    }
  };

  const handleOidcLogin = () => {
    login(window.location.pathname);
  };

  if (!isOpen) {
    return null;
  }

  const isSignUp = mode === "signup";

  return (
    <div className={styles.overlay} onClick={onClose}>
      <div className={styles.dialog} onClick={(e) => e.stopPropagation()}>
        <div className={styles.header}>
          <h2>{isSignUp ? t("workspace.signUp") : t("workspace.login")}</h2>
          <button className={styles.close} onClick={onClose} aria-label="Close">
            <svg
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
            >
              <path d="M18 6L6 18M6 6l12 12" />
            </svg>
          </button>
        </div>

        <div className={styles.content}>
          {/* Local login/signup form */}
          {localAuthEnabled && (
            <form
              onSubmit={isSignUp ? handleRegister : handleLocalLogin}
              className={styles.form}
            >
              {isSignUp && (
                <div className={styles.field}>
                  <label htmlFor={`${formId}-name`}>
                    {t("workspace.name")}
                  </label>
                  <input
                    id={`${formId}-name`}
                    name="name"
                    type="text"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    onKeyDown={stopPropagation}
                    onKeyUp={stopPropagation}
                    placeholder={t("workspace.namePlaceholder")}
                  />
                </div>
              )}

              <div className={styles.field}>
                <label htmlFor={`${formId}-email`}>
                  {t("workspace.email")}
                </label>
                <input
                  id={`${formId}-email`}
                  name="email"
                  type={isSignUp ? "email" : "text"}
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  onKeyDown={stopPropagation}
                  onKeyUp={stopPropagation}
                  placeholder={isSignUp ? "you@example.com" : "admin"}
                  required
                  autoFocus
                />
              </div>

              <div className={styles.field}>
                <label htmlFor={`${formId}-password`}>
                  {t("workspace.password")}
                </label>
                <input
                  id={`${formId}-password`}
                  name="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  onKeyDown={stopPropagation}
                  onKeyUp={stopPropagation}
                  placeholder="••••••••"
                  required
                />
              </div>

              {isSignUp && (
                <div className={styles.field}>
                  <label htmlFor={`${formId}-confirmPassword`}>
                    {t("workspace.confirmPassword")}
                  </label>
                  <input
                    id={`${formId}-confirmPassword`}
                    name="confirmPassword"
                    type="password"
                    value={confirmPassword}
                    onChange={(e) => setConfirmPassword(e.target.value)}
                    onKeyDown={stopPropagation}
                    onKeyUp={stopPropagation}
                    placeholder="••••••••"
                    required
                  />
                </div>
              )}

              {error && <div className={styles.error}>{error}</div>}

              <button
                type="submit"
                className={styles.submit}
                disabled={isLoading}
              >
                {isLoading
                  ? isSignUp
                    ? t("workspace.signingUp")
                    : t("workspace.loggingIn")
                  : isSignUp
                  ? t("workspace.signUp")
                  : t("workspace.login")}
              </button>
            </form>
          )}

          {/* Toggle between sign in and sign up */}
          {localAuthEnabled && registrationEnabled && (
            <div className={styles.toggle}>
              {isSignUp ? (
                <span>
                  {t("workspace.alreadyHaveAccount")}{" "}
                  <button
                    type="button"
                    className={styles.toggleLink}
                    onClick={() => handleModeSwitch("signin")}
                  >
                    {t("workspace.login")}
                  </button>
                </span>
              ) : (
                <span>
                  {t("workspace.noAccount")}{" "}
                  <button
                    type="button"
                    className={styles.toggleLink}
                    onClick={() => handleModeSwitch("signup")}
                  >
                    {t("workspace.signUp")}
                  </button>
                </span>
              )}
            </div>
          )}

          {/* OIDC login option */}
          {oidcConfigured && localAuthEnabled && (
            <div className={styles.divider}>
              <span>{t("workspace.or")}</span>
            </div>
          )}

          {oidcConfigured && (
            <button className={styles.oidcButton} onClick={handleOidcLogin}>
              <svg
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
              >
                <path d="M15 3h4a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-4M10 17l5-5-5-5M15 12H3" />
              </svg>
              <span>{t("workspace.loginWithOIDC")}</span>
            </button>
          )}

          {/* Hint for default credentials (only show for sign in when registration is disabled) */}
          {localAuthEnabled &&
            !oidcConfigured &&
            !isSignUp &&
            !registrationEnabled && (
              <div className={styles.hint}>
                {t("workspace.defaultCredentialsHint")}
              </div>
            )}
        </div>
      </div>
    </div>
  );
};

export default LoginDialog;
