import { Footer, FooterLeftExtra } from "@excalidraw/excalidraw/index";
import {
  presentationIcon,
  videoIcon,
  commentsIcon,
} from "@excalidraw/excalidraw/components/icons";
import { Tooltip } from "@excalidraw/excalidraw/components/Tooltip";
import { t } from "@excalidraw/excalidraw/i18n";
import React from "react";

import type { ExcalidrawImperativeAPI } from "@excalidraw/excalidraw/types";

import { isExcalidrawPlusSignedUser } from "../../app_constants";

import { DebugFooter, isVisualDebuggerEnabled } from "../DebugCanvas";
import { EncryptedIcon } from "../EncryptedIcon";

import styles from "./AppFooter.module.scss";

export interface AppFooterProps {
  onChange: () => void;
  excalidrawAPI: ExcalidrawImperativeAPI | null;
}

export const AppFooter = React.memo(
  ({ onChange, excalidrawAPI }: AppFooterProps) => {
    const handleTogglePresentationSidebar = () => {
      if (!excalidrawAPI) {
        return;
      }

      const appState = excalidrawAPI.getAppState();
      const sidebarIsOpen = appState.openSidebar?.name === "default";

      if (sidebarIsOpen) {
        // Sidebar is open - close it
        excalidrawAPI.updateScene({
          appState: { openSidebar: null },
        });
      } else {
        // Sidebar is closed - open to presentation tab
        excalidrawAPI.updateScene({
          appState: { openSidebar: { name: "default", tab: "presentation" } },
        });
      }
    };

    const handleToggleTalktrackSidebar = () => {
      if (!excalidrawAPI) {
        return;
      }

      const appState = excalidrawAPI.getAppState();
      const sidebarIsOpen =
        appState.openSidebar?.name === "default" &&
        appState.openSidebar?.tab === "talktrack";

      if (sidebarIsOpen) {
        // Sidebar is open to talktrack - close it
        excalidrawAPI.updateScene({
          appState: { openSidebar: null },
        });
      } else {
        // Sidebar is closed or on different tab - open to talktrack tab
        excalidrawAPI.updateScene({
          appState: { openSidebar: { name: "default", tab: "talktrack" } },
        });
      }
    };

    const handleToggleCommentsSidebar = () => {
      if (!excalidrawAPI) {
        return;
      }

      const appState = excalidrawAPI.getAppState();
      const sidebarIsOpen =
        appState.openSidebar?.name === "default" &&
        appState.openSidebar?.tab === "comments";

      if (sidebarIsOpen) {
        // Sidebar is open to comments - close it
        excalidrawAPI.updateScene({
          appState: { openSidebar: null },
        });
      } else {
        // Sidebar is closed or on different tab - open to comments tab
        excalidrawAPI.updateScene({
          appState: { openSidebar: { name: "default", tab: "comments" } },
        });
      }
    };

    return (
      <>
        {/* Presentation and Talktrack toggle buttons - positioned after undo/redo in footer left */}
        <FooterLeftExtra>
          <Tooltip label={t("presentation.title")} compact>
            <button
              className={styles.sidebarButton}
              onClick={handleTogglePresentationSidebar}
              onPointerDown={(e) => e.stopPropagation()}
              type="button"
              aria-label={t("presentation.title")}
            >
              <div className={styles.toolIconWrapper} aria-hidden="true">
                {presentationIcon}
              </div>
            </button>
          </Tooltip>

          <Tooltip label={t("talktrack.title")} compact>
            <button
              className={styles.sidebarButton}
              onClick={handleToggleTalktrackSidebar}
              onPointerDown={(e) => e.stopPropagation()}
              type="button"
              aria-label={t("talktrack.title")}
            >
              <div className={styles.toolIconWrapper} aria-hidden="true">
                {videoIcon}
              </div>
            </button>
          </Tooltip>

          <Tooltip label={t("buttons.comments")} compact>
            <button
              className={styles.sidebarButton}
              onClick={handleToggleCommentsSidebar}
              onPointerDown={(e) => e.stopPropagation()}
              type="button"
              aria-label={t("buttons.comments")}
            >
              <div className={styles.toolIconWrapper} aria-hidden="true">
                {commentsIcon}
              </div>
            </button>
          </Tooltip>
        </FooterLeftExtra>

        {/* Center footer content */}
        <Footer>
          <div
            style={{
              display: "flex",
              gap: ".5rem",
              alignItems: "center",
            }}
          >
            {isVisualDebuggerEnabled() && <DebugFooter onChange={onChange} />}
            {!isExcalidrawPlusSignedUser && <EncryptedIcon />}
          </div>
        </Footer>
      </>
    );
  },
);
