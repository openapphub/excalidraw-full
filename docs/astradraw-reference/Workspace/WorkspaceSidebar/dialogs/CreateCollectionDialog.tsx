import React from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import { EmojiPicker } from "../../../EmojiPicker";
import styles from "../WorkspaceSidebar.module.scss";

interface CreateCollectionDialogProps {
  isOpen: boolean;
  name: string;
  icon: string;
  onNameChange: (name: string) => void;
  onIconChange: (icon: string) => void;
  onSubmit: () => void;
  onClose: () => void;
}

export const CreateCollectionDialog: React.FC<CreateCollectionDialogProps> = ({
  isOpen,
  name,
  icon,
  onNameChange,
  onIconChange,
  onSubmit,
  onClose,
}) => {
  if (!isOpen) {
    return null;
  }

  return (
    <div className={styles.dialogOverlay} onClick={onClose}>
      <div className={styles.dialog} onClick={(e) => e.stopPropagation()}>
        <h3>{t("workspace.createCollection")}</h3>
        <div className={styles.dialogContent}>
          <div className={styles.formRow}>
            <div className={`${styles.formGroup} ${styles.formGroupIcon}`}>
              <label>{t("workspace.icon")}</label>
              <EmojiPicker value={icon} onSelect={onIconChange} />
            </div>
            <div className={`${styles.formGroup} ${styles.formGroupName}`}>
              <label>{t("workspace.collectionName")}</label>
              <input
                type="text"
                value={name}
                onChange={(e) => onNameChange(e.target.value)}
                placeholder={t("workspace.collectionNamePlaceholder")}
                onKeyDown={(e) => {
                  e.stopPropagation();
                  if (e.key === "Enter") {
                    onSubmit();
                  }
                }}
                onKeyUp={(e) => e.stopPropagation()}
                autoFocus
              />
            </div>
          </div>
        </div>
        <div className={styles.dialogActions}>
          <button className={styles.dialogCancel} onClick={onClose}>
            {t("workspace.cancel")}
          </button>
          <button
            className={styles.dialogConfirm}
            onClick={onSubmit}
            disabled={!name.trim()}
          >
            {t("workspace.createCollection")}
          </button>
        </div>
      </div>
    </div>
  );
};

export default CreateCollectionDialog;
