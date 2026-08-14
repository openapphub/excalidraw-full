import React from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import styles from "../WorkspaceSidebar.module.scss";

import type { WorkspaceType } from "../../../../auth/workspaceApi";

interface CreateWorkspaceDialogProps {
  isOpen: boolean;
  name: string;
  slug: string;
  type: WorkspaceType;
  error: string | null;
  isCreating: boolean;
  onNameChange: (name: string) => void;
  onSlugChange: (slug: string) => void;
  onTypeChange: (type: WorkspaceType) => void;
  onSubmit: () => void;
  onClose: () => void;
}

export const CreateWorkspaceDialog: React.FC<CreateWorkspaceDialogProps> = ({
  isOpen,
  name,
  slug,
  type,
  error,
  isCreating,
  onNameChange,
  onSlugChange,
  onTypeChange,
  onSubmit,
  onClose,
}) => {
  if (!isOpen) {
    return null;
  }

  return (
    <div className={styles.dialogOverlay} onClick={onClose}>
      <div
        className={`${styles.dialog} ${styles.dialogWide}`}
        onClick={(e) => e.stopPropagation()}
      >
        <h3>{t("workspace.createWorkspaceTitle")}</h3>
        <div className={styles.dialogContent}>
          <div className={styles.formGroup}>
            <label>{t("workspace.workspaceNameLabel")}</label>
            <input
              type="text"
              value={name}
              onChange={(e) => onNameChange(e.target.value)}
              placeholder={t("workspace.workspaceNamePlaceholder")}
              onKeyDown={(e) => {
                e.stopPropagation();
                if (e.key === "Enter" && !isCreating) {
                  onSubmit();
                }
              }}
              onKeyUp={(e) => e.stopPropagation()}
              autoFocus
            />
          </div>
          <div className={styles.formGroup}>
            <label>{t("workspace.workspaceSlugLabel")}</label>
            <input
              type="text"
              value={slug}
              onChange={(e) => onSlugChange(e.target.value)}
              placeholder="my-workspace"
              onKeyDown={(e) => {
                e.stopPropagation();
                if (e.key === "Enter" && !isCreating) {
                  onSubmit();
                }
              }}
              onKeyUp={(e) => e.stopPropagation()}
            />
            <span className={styles.formHint}>
              {t("workspace.workspaceSlugHint")}
            </span>
          </div>
          <div className={styles.formGroup}>
            <label>{t("workspace.workspaceTypeLabel")}</label>
            <div className={styles.typeSelector}>
              <button
                type="button"
                className={`${styles.typeOption} ${
                  type === "PERSONAL" ? styles.typeOptionActive : ""
                }`}
                onClick={() => onTypeChange("PERSONAL")}
              >
                {t("workspace.workspaceTypePersonal")}
              </button>
              <button
                type="button"
                className={`${styles.typeOption} ${
                  type === "SHARED" ? styles.typeOptionActive : ""
                }`}
                onClick={() => onTypeChange("SHARED")}
              >
                {t("workspace.workspaceTypeShared")}
              </button>
            </div>
          </div>
          {error && <div className={styles.formError}>{error}</div>}
        </div>
        <div className={styles.dialogActions}>
          <button className={styles.dialogCancel} onClick={onClose}>
            {t("workspace.cancel")}
          </button>
          <button
            className={styles.dialogConfirm}
            onClick={onSubmit}
            disabled={!name.trim() || !slug.trim() || isCreating}
          >
            {isCreating
              ? t("workspace.creating")
              : t("workspace.createWorkspace")}
          </button>
        </div>
      </div>
    </div>
  );
};

export default CreateWorkspaceDialog;
