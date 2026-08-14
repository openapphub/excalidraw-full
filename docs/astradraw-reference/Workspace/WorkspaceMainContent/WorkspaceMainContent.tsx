import React from "react";

import type { Theme } from "@excalidraw/element/types";

import { useAtomValue } from "../../../app-jotai";

import {
  dashboardViewAtom,
  searchQueryAtom,
  currentWorkspaceAtom,
  activeCollectionAtom,
} from "../../Settings/settingsState";

import { ProfilePage } from "../../Settings/ProfilePage";
import { PreferencesPage } from "../../Settings/PreferencesPage";
import { WorkspaceSettingsPage } from "../../Settings/WorkspaceSettingsPage";
import { MembersPage } from "../../Settings/MembersPage";
import { TeamsCollectionsPage } from "../../Settings/TeamsCollectionsPage";
import { NotificationsPage } from "../../Notifications/NotificationsPage";

import { DashboardView } from "../DashboardView";
import { CollectionView } from "../CollectionView";
import { SearchResultsView } from "../SearchResultsView";

import styles from "./WorkspaceMainContent.module.scss";

export interface WorkspaceMainContentProps {
  isAdmin: boolean;
  onNewScene: (collectionId?: string) => void;
  onUpdateWorkspace?: (data: { name?: string }) => Promise<void>;
  onUploadWorkspaceAvatar?: (file: File) => Promise<void>;
  onDeleteWorkspace?: () => Promise<void>;
  theme: Theme | "system";
  setTheme: (theme: Theme | "system") => void;
}

export const WorkspaceMainContent: React.FC<WorkspaceMainContentProps> = ({
  isAdmin,
  onNewScene,
  onUpdateWorkspace,
  onUploadWorkspaceAvatar,
  onDeleteWorkspace,
  theme,
  setTheme,
}) => {
  // Read workspace and collections from Jotai atoms
  const workspace = useAtomValue(currentWorkspaceAtom);
  const activeCollection = useAtomValue(activeCollectionAtom);
  const dashboardView = useAtomValue(dashboardViewAtom);
  const searchQuery = useAtomValue(searchQueryAtom);

  // If there's a search query, show search results instead of normal content
  if (searchQuery.trim()) {
    return (
      <div className={styles.container}>
        <SearchResultsView />
      </div>
    );
  }

  const renderContent = () => {
    switch (dashboardView) {
      case "home":
        return <DashboardView onNewScene={onNewScene} />;
      case "collection":
        return (
          <CollectionView
            collection={activeCollection}
            onNewScene={onNewScene}
          />
        );
      case "profile":
        return <ProfilePage />;
      case "preferences":
        return <PreferencesPage theme={theme} setTheme={setTheme} />;
      case "workspace":
        return (
          <WorkspaceSettingsPage
            workspace={workspace}
            onUpdateWorkspace={onUpdateWorkspace}
            onUploadWorkspaceAvatar={onUploadWorkspaceAvatar}
            onDeleteWorkspace={onDeleteWorkspace}
          />
        );
      case "members":
        return (
          <MembersPage workspaceId={workspace?.id || null} isAdmin={isAdmin} />
        );
      case "teams-collections":
        return (
          <TeamsCollectionsPage
            workspaceId={workspace?.id || null}
            isAdmin={isAdmin}
          />
        );
      case "notifications":
        return <NotificationsPage />;
      default:
        return <DashboardView onNewScene={onNewScene} />;
    }
  };

  return <div className={styles.container}>{renderContent()}</div>;
};

export default WorkspaceMainContent;
