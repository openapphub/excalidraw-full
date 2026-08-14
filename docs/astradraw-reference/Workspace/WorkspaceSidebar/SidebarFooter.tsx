import React from "react";

import { NotificationBell } from "../../Notifications";

import styles from "./WorkspaceSidebar.module.scss";

import type { User } from "../../../auth";

interface SidebarFooterProps {
  user: User;
  /** Current workspace slug for notification URLs */
  workspaceSlug?: string;
  /** Current app mode - determines notification bell behavior */
  appMode?: "canvas" | "dashboard";
  onProfileClick: () => void;
  /** Callback when navigating to a notification */
  onNotificationNavigate?: (
    sceneId: string,
    threadId?: string,
    commentId?: string,
  ) => void;
}

const getInitials = (name: string | null, email: string): string => {
  if (name) {
    return name
      .split(" ")
      .map((n) => n[0])
      .join("")
      .toUpperCase()
      .slice(0, 2);
  }
  return email[0].toUpperCase();
};

export const SidebarFooter: React.FC<SidebarFooterProps> = ({
  user,
  workspaceSlug,
  appMode = "canvas",
  onProfileClick,
  onNotificationNavigate,
}) => {
  return (
    <>
      <div className={styles.divider} />
      <div className={styles.footer}>
        <button className={styles.userButton} onClick={onProfileClick}>
          {user.avatarUrl ? (
            <img
              src={user.avatarUrl}
              alt={user.name || user.email}
              className={styles.userAvatar}
            />
          ) : (
            <div
              className={`${styles.userAvatar} ${styles.userAvatarInitials}`}
            >
              {getInitials(user.name, user.email)}
            </div>
          )}
          <span className={styles.userName}>{user.name || user.email}</span>
        </button>
        <NotificationBell
          workspaceSlug={workspaceSlug}
          enabled={true}
          appMode={appMode}
          onNavigate={onNotificationNavigate}
        />
      </div>
    </>
  );
};

export default SidebarFooter;
