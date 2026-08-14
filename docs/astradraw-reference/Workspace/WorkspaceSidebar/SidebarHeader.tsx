import React, { useState, useEffect, useRef } from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import { useAtomValue } from "../../../app-jotai";
import {
  currentWorkspaceAtom,
  workspacesAtom,
} from "../../Settings/settingsState";

import { chevronIcon, closeIcon } from "./icons";
import styles from "./WorkspaceSidebar.module.scss";

import type { Workspace } from "../../../auth/workspaceApi";
import type { User } from "../../../auth";

interface SidebarHeaderProps {
  isAuthenticated: boolean;
  user: User | null;
  onSwitchWorkspace: (workspace: Workspace) => void;
  onCreateWorkspaceClick: () => void;
  onClose: () => void;
}

export const SidebarHeader: React.FC<SidebarHeaderProps> = ({
  isAuthenticated,
  user,
  onSwitchWorkspace,
  onCreateWorkspaceClick,
  onClose,
}) => {
  // Read workspace data from Jotai atoms
  const currentWorkspace = useAtomValue(currentWorkspaceAtom);
  const workspaces = useAtomValue(workspacesAtom) as Workspace[];

  const [workspaceMenuOpen, setWorkspaceMenuOpen] = useState(false);
  const workspaceMenuRef = useRef<HTMLDivElement>(null);

  // Close menu when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (
        workspaceMenuRef.current &&
        !workspaceMenuRef.current.contains(event.target as Node)
      ) {
        setWorkspaceMenuOpen(false);
      }
    };

    if (workspaceMenuOpen) {
      document.addEventListener("mousedown", handleClickOutside);
    }

    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
    };
  }, [workspaceMenuOpen]);

  return (
    <div className={styles.header}>
      {isAuthenticated && currentWorkspace ? (
        <div className={styles.workspace} ref={workspaceMenuRef}>
          <button
            className={styles.workspaceTrigger}
            onClick={() => setWorkspaceMenuOpen(!workspaceMenuOpen)}
            aria-expanded={workspaceMenuOpen}
          >
            <div className={styles.workspaceAvatar}>
              {currentWorkspace.avatarUrl ? (
                <img
                  src={currentWorkspace.avatarUrl}
                  alt={currentWorkspace.name}
                />
              ) : (
                <span>{currentWorkspace.name[0].toUpperCase()}</span>
              )}
            </div>
            <span className={styles.workspaceName}>
              {currentWorkspace.name}
            </span>
            <span
              className={`${styles.chevron} ${
                workspaceMenuOpen ? styles.chevronOpen : ""
              }`}
            >
              {chevronIcon}
            </span>
          </button>

          {workspaceMenuOpen && (
            <div className={styles.workspaceMenu}>
              <div className={styles.menuSectionTitle}>
                {t("workspace.switchWorkspace")}
              </div>
              {workspaces.map((ws) => (
                <button
                  key={ws.id}
                  className={`${styles.menuItem} ${
                    ws.id === currentWorkspace.id ? styles.menuItemActive : ""
                  }`}
                  onClick={() => {
                    onSwitchWorkspace(ws);
                    setWorkspaceMenuOpen(false);
                  }}
                >
                  <div
                    className={`${styles.workspaceAvatar} ${styles.workspaceAvatarSmall}`}
                  >
                    {ws.avatarUrl ? (
                      <img src={ws.avatarUrl} alt={ws.name} />
                    ) : (
                      <span>{ws.name[0].toUpperCase()}</span>
                    )}
                  </div>
                  <span>{ws.name}</span>
                  {ws.id === currentWorkspace.id && (
                    <span className={styles.check}>✓</span>
                  )}
                </button>
              ))}
              {user?.isSuperAdmin && (
                <>
                  <div className={styles.menuDivider} />
                  <button
                    className={`${styles.menuItem} ${styles.menuItemCreate}`}
                    onClick={() => {
                      onCreateWorkspaceClick();
                      setWorkspaceMenuOpen(false);
                    }}
                  >
                    <span className={styles.menuItemIcon}>+</span>
                    <span>{t("workspace.createWorkspace")}</span>
                  </button>
                </>
              )}
            </div>
          )}
        </div>
      ) : (
        <h2 className={styles.title}>
          <span>{t("workspace.title")}</span>
        </h2>
      )}
      <button className={styles.close} onClick={onClose} aria-label="Close">
        {closeIcon}
      </button>
    </div>
  );
};

export default SidebarHeader;
