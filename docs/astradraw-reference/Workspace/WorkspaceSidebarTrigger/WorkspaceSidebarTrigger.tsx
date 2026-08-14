import React, { useEffect, useCallback } from "react";
import { t } from "@excalidraw/excalidraw/i18n";
import { useTunnels, useUIAppState } from "@excalidraw/excalidraw";

import { useAtomValue, useSetAtom } from "../../../app-jotai";
import { useAuth } from "../../../auth";
import {
  workspaceSidebarOpenAtom,
  toggleWorkspaceSidebarAtom,
} from "../../Settings/settingsState";

import styles from "./WorkspaceSidebarTrigger.module.scss";

// Sidebar panel icon (like Excalidraw+)
const sidebarIcon = (
  <svg viewBox="0 0 24 24" fill="none">
    <path
      d="M14.807 9.249a.75.75 0 0 0-1.059-.056l-2.5 2.25a.75.75 0 0 0 0 1.114l2.5 2.25a.75.75 0 0 0 1.004-1.115l-1.048-.942h3.546a.75.75 0 1 0 0-1.5h-3.546l1.048-.942a.75.75 0 0 0 .055-1.059ZM2 17.251A2.75 2.75 0 0 0 4.75 20h14.5A2.75 2.75 0 0 0 22 17.25V6.75A2.75 2.75 0 0 0 19.25 4H4.75A2.75 2.75 0 0 0 2 6.75v10.5Zm2.75 1.25c-.69 0-1.25-.56-1.25-1.25V6.749c0-.69.56-1.25 1.25-1.25h3.254V18.5H4.75Zm4.754 0V5.5h9.746c.69 0 1.25.56 1.25 1.25v10.5c0 .69-.56 1.25-1.25 1.25H9.504Z"
      fill="currentColor"
    />
  </svg>
);

export const WorkspaceSidebarTrigger: React.FC = () => {
  const { isAuthenticated } = useAuth();
  const { WorkspaceTriggerTunnel } = useTunnels();
  const appState = useUIAppState();

  // Hide trigger in presentation mode
  const isPresentationMode = appState.presentationMode?.active ?? false;

  // Sidebar state from Jotai atoms
  const isOpen = useAtomValue(workspaceSidebarOpenAtom);
  const toggleSidebar = useSetAtom(toggleWorkspaceSidebarAtom);

  // Handle backtick keyboard shortcut (Cmd+[ is handled in App.tsx with capture phase)
  const handleKeyDown = useCallback(
    (e: KeyboardEvent) => {
      // Don't trigger if user is typing in an input
      const target = e.target as HTMLElement;
      if (
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.isContentEditable
      ) {
        return;
      }

      // Backtick key to toggle sidebar (no modifiers)
      if (e.key === "`" && !e.metaKey && !e.ctrlKey && !e.altKey) {
        e.preventDefault();
        toggleSidebar();
      }
    },
    [toggleSidebar],
  );

  useEffect(() => {
    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [handleKeyDown]);

  const buttonClasses = [
    styles.button,
    isOpen ? styles.active : "",
    isAuthenticated ? styles.authenticated : "",
    !isOpen ? styles.closed : "",
  ]
    .filter(Boolean)
    .join(" ");

  // Don't render in presentation mode
  if (isPresentationMode) {
    return null;
  }

  // Render through tunnel to place before hamburger menu
  return (
    <WorkspaceTriggerTunnel.In>
      <div className={styles.trigger}>
        <button
          type="button"
          className={buttonClasses}
          onClick={toggleSidebar}
          aria-label={t("workspace.title")}
          aria-pressed={isOpen}
          title={`${t("workspace.title")} (\` or ⌘[)`}
        >
          {sidebarIcon}
        </button>
      </div>
    </WorkspaceTriggerTunnel.In>
  );
};

export default WorkspaceSidebarTrigger;
