import React, { useCallback, useMemo } from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import { useAtomValue, useSetAtom } from "../../../app-jotai";
import { type WorkspaceScene } from "../../../auth/workspaceApi";
import {
  navigateToCanvasAtom,
  navigateToSceneAtom,
  currentWorkspaceSlugAtom,
  currentWorkspaceAtom,
} from "../../Settings/settingsState";
import { useScenesCache } from "../../../hooks/useScenesCache";
import { useSceneActions } from "../../../hooks/useSceneActions";

import { SceneCardSkeletonGrid } from "../../Skeletons";

import { SceneCardGrid } from "../SceneCardGrid";

import styles from "./DashboardView.module.scss";

// Icons
const dashboardIcon = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
    <rect x="3" y="3" width="7" height="7" />
    <rect x="14" y="3" width="7" height="7" />
    <rect x="14" y="14" width="7" height="7" />
    <rect x="3" y="14" width="7" height="7" />
  </svg>
);

const plusIcon = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
    <path d="M12 5v14M5 12h14" />
  </svg>
);

interface DashboardViewProps {
  onNewScene: (collectionId?: string) => void;
}

export const DashboardView: React.FC<DashboardViewProps> = ({ onNewScene }) => {
  // Read workspace from Jotai atom
  const workspace = useAtomValue(currentWorkspaceAtom);
  const navigateToCanvas = useSetAtom(navigateToCanvasAtom);
  const navigateToScene = useSetAtom(navigateToSceneAtom);
  const workspaceSlug = useAtomValue(currentWorkspaceSlugAtom);

  // Use shared scenes cache - fetch all scenes for workspace (no collection filter)
  const { scenes: allScenes, isLoading } = useScenesCache({
    workspaceId: workspace?.id,
    collectionId: null, // null = all scenes
    enabled: !!workspace?.id,
  });

  // Scene actions hook - centralized delete/rename/duplicate logic with optimistic updates
  const { deleteScene, renameScene, duplicateScene } = useSceneActions({
    workspaceId: workspace?.id,
    collectionId: null,
  });

  // Derive recently modified and visited from cached scenes
  const { recentlyModified, recentlyVisited } = useMemo(() => {
    // Sort by updatedAt for recently modified
    const sortedByModified = [...allScenes].sort(
      (a, b) =>
        new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime(),
    );

    return {
      recentlyModified: sortedByModified.slice(0, 8),
      // For recently visited, we'd need a separate API endpoint
      // For now, use the same data but could be different in future
      recentlyVisited: sortedByModified.slice(0, 6),
    };
  }, [allScenes]);

  // Navigate to scene via URL - this triggers the popstate handler which loads the scene
  const handleOpenScene = useCallback(
    (scene: WorkspaceScene) => {
      if (workspaceSlug) {
        navigateToScene({
          sceneId: scene.id,
          title: scene.title,
          workspaceSlug,
        });
      }
    },
    [navigateToScene, workspaceSlug],
  );

  if (isLoading) {
    return (
      <div className={styles.view}>
        {/* Header */}
        <header className={styles.header}>
          <div className={styles.titleRow}>
            <span className={styles.icon}>{dashboardIcon}</span>
            <h1 className={styles.title}>{t("workspace.dashboard")}</h1>
          </div>
        </header>

        {/* Separator after header */}
        <div className={styles.separator} />

        {/* Loading skeleton for recently modified section */}
        <section className={styles.section}>
          <h2 className={styles.sectionTitle}>
            {t("workspace.recentlyModified")}
          </h2>
          <SceneCardSkeletonGrid count={6} />
        </section>
      </div>
    );
  }

  return (
    <div className={styles.view}>
      {/* Header */}
      <header className={styles.header}>
        <div className={styles.titleRow}>
          <div className={styles.titleGroup}>
            <span className={styles.icon}>{dashboardIcon}</span>
            <h1 className={styles.title}>{t("workspace.dashboard")}</h1>
          </div>
          <button
            className={styles.startButton}
            onClick={() => {
              navigateToCanvas();
              onNewScene();
            }}
          >
            {plusIcon}
            <span>{t("workspace.startDrawing")}</span>
          </button>
        </div>
        <p className={styles.tip}>
          <strong>{t("workspace.tip")}</strong> {t("workspace.changeHomePage")}{" "}
          <a href="#preferences">{t("workspace.preferences")}</a>
        </p>
      </header>

      {/* Separator after header */}
      <div className={styles.separator} />

      {/* Recently modified section */}
      <section className={styles.section}>
        <h2 className={styles.sectionTitle}>
          {t("workspace.recentlyModified")}
        </h2>
        <SceneCardGrid
          scenes={recentlyModified}
          onOpenScene={handleOpenScene}
          onDeleteScene={deleteScene}
          onRenameScene={renameScene}
          onDuplicateScene={duplicateScene}
          emptyMessage={t("workspace.noScenes")}
          emptyHint={t("workspace.noScenesHint")}
        />
      </section>

      {/* Recently visited section */}
      <section className={styles.section}>
        <h2 className={styles.sectionTitle}>
          {t("workspace.recentlyVisited")}
        </h2>
        <SceneCardGrid
          scenes={recentlyVisited}
          onOpenScene={handleOpenScene}
          onDeleteScene={deleteScene}
          onRenameScene={renameScene}
          onDuplicateScene={duplicateScene}
          emptyMessage={t("workspace.noRecentlyVisited")}
        />
      </section>

      {/* Team members section (placeholder) - only for shared workspaces */}
      {workspace?.type !== "PERSONAL" && (
        <section className={styles.section}>
          <h2 className={styles.sectionTitle}>
            {t("workspace.teamMembersAt")}
          </h2>
          <div className={styles.teamEmpty}>
            <div className={styles.teamEmptyIllustration}>
              {/* Paper airplane doodle */}
              <svg
                viewBox="0 0 200 120"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeDasharray="4 4"
              >
                <path d="M20 80 Q60 40 100 60 Q140 80 180 40" />
                <path d="M170 35 L180 40 L175 50" />
              </svg>
            </div>
            <p className={styles.teamEmptyText}>{t("workspace.noOneActive")}</p>
          </div>
        </section>
      )}
    </div>
  );
};

export default DashboardView;
