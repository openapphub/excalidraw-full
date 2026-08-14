import React, { useState, useCallback } from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import { useAtomValue, useSetAtom } from "../../../app-jotai";
import {
  type WorkspaceScene,
  type Collection,
} from "../../../auth/workspaceApi";
import {
  navigateToCanvasAtom,
  navigateToSceneAtom,
  currentWorkspaceAtom,
} from "../../Settings/settingsState";
import { useScenesCache } from "../../../hooks/useScenesCache";
import { useSceneActions } from "../../../hooks/useSceneActions";

import { SceneCardSkeletonGrid } from "../../Skeletons";

import { SceneCardGrid } from "../SceneCardGrid";

import styles from "./CollectionView.module.scss";

// Icons
const lockIcon = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
    <rect x="3" y="11" width="18" height="11" rx="2" ry="2" />
    <path d="M7 11V7a5 5 0 0 1 10 0v4" />
  </svg>
);

const folderIcon = (
  <svg viewBox="0 0 24 24" fill="none" aria-hidden="true">
    <path
      d="M20.496 5.627A2.25 2.25 0 0 1 22 7.75v10A4.25 4.25 0 0 1 17.75 22h-10a2.25 2.25 0 0 1-2.123-1.504l2.097.004H17.75a2.75 2.75 0 0 0 2.75-2.75v-10l-.004-.051V5.627ZM17.246 2a2.25 2.25 0 0 1 2.25 2.25v12.997a2.25 2.25 0 0 1-2.25 2.25H4.25A2.25 2.25 0 0 1 2 17.247V4.25A2.25 2.25 0 0 1 4.25 2h12.997Zm0 1.5H4.25a.75.75 0 0 0-.75.75v12.997c0 .414.336.75.75.75h12.997a.75.75 0 0 0 .75-.75V4.25a.75.75 0 0 0-.75-.75Z"
      fill="currentColor"
    />
  </svg>
);

const importIcon = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
    <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M17 8l-5-5-5 5M12 3v12" />
  </svg>
);

const plusIcon = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
    <path d="M12 5v14M5 12h14" />
  </svg>
);

const sortIcon = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
    <path d="M11 5h10M11 9h7M11 13h4M3 17l3 3 3-3M6 18V4" />
  </svg>
);

interface CollectionViewProps {
  collection: Collection | null;
  onNewScene: (collectionId?: string) => void;
}

export const CollectionView: React.FC<CollectionViewProps> = ({
  collection,
  onNewScene,
}) => {
  // Read workspace from Jotai atom
  const workspace = useAtomValue(currentWorkspaceAtom);
  const navigateToCanvas = useSetAtom(navigateToCanvasAtom);
  const navigateToScene = useSetAtom(navigateToSceneAtom);
  // Use workspace.slug directly instead of separate atom to ensure consistency
  const workspaceSlug = workspace?.slug;

  const [sortBy, setSortBy] = useState<"created" | "modified">("created");
  const [showSortMenu, setShowSortMenu] = useState(false);

  // Use shared scenes cache for this collection
  const { scenes, isLoading } = useScenesCache({
    workspaceId: workspace?.id,
    collectionId: collection?.id,
    enabled: !!workspace?.id && !!collection?.id,
  });

  // Scene actions hook - centralized delete/rename/duplicate logic with optimistic updates
  const { deleteScene, renameScene, duplicateScene } = useSceneActions({
    workspaceId: workspace?.id,
    collectionId: collection?.id,
  });

  // Sort scenes
  const sortedScenes = React.useMemo(() => {
    const sorted = [...scenes];
    if (sortBy === "created") {
      sorted.sort(
        (a, b) =>
          new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime(),
      );
    } else {
      sorted.sort(
        (a, b) =>
          new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime(),
      );
    }
    return sorted;
  }, [scenes, sortBy]);

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

  const handleImportScenes = useCallback(() => {
    // TODO: Implement import dialog
    // eslint-disable-next-line no-console
    console.log("Import scenes - not yet implemented");
  }, []);

  const handleCreateScene = useCallback(() => {
    navigateToCanvas();
    onNewScene(collection?.id);
  }, [navigateToCanvas, onNewScene, collection]);

  // Get collection display info
  const collectionName = collection?.isPrivate
    ? t("workspace.private")
    : collection?.name || t("workspace.untitled");
  const collectionDescription = collection?.isPrivate
    ? t("workspace.privateDescription")
    : null;

  // Render icon with default styling if no emoji icon
  const renderCollectionIcon = () => {
    if (collection?.isPrivate) {
      return <span className={styles.icon}>{lockIcon}</span>;
    }
    if (collection?.icon) {
      return <span className={styles.icon}>{collection.icon}</span>;
    }
    // Default SVG icon with circular background
    return (
      <span className={`${styles.icon} ${styles.iconDefault}`}>
        {folderIcon}
      </span>
    );
  };

  if (isLoading) {
    return (
      <div className={styles.view}>
        {/* Header */}
        <header className={styles.header}>
          <div className={styles.titleRow}>
            {renderCollectionIcon()}
            <h1 className={styles.title}>{collectionName}</h1>
          </div>
        </header>

        {/* Separator after header */}
        <div className={styles.separator} />

        {/* Loading skeleton */}
        <div className={styles.content}>
          <SceneCardSkeletonGrid count={6} />
        </div>
      </div>
    );
  }

  return (
    <div className={styles.view}>
      {/* Header */}
      <header className={styles.header}>
        <div className={styles.titleRow}>
          {renderCollectionIcon()}
          <h1 className={styles.title}>{collectionName}</h1>

          {/* Sort dropdown */}
          <div className={styles.sort}>
            <button
              className={styles.sortTrigger}
              onClick={() => setShowSortMenu(!showSortMenu)}
            >
              {sortBy === "created"
                ? t("workspace.lastCreated")
                : t("workspace.lastModified")}
              {sortIcon}
            </button>
            {showSortMenu && (
              <div className={styles.sortMenu}>
                <button
                  className={sortBy === "created" ? styles.sortMenuActive : ""}
                  onClick={() => {
                    setSortBy("created");
                    setShowSortMenu(false);
                  }}
                >
                  {t("workspace.lastCreated")}
                </button>
                <button
                  className={sortBy === "modified" ? styles.sortMenuActive : ""}
                  onClick={() => {
                    setSortBy("modified");
                    setShowSortMenu(false);
                  }}
                >
                  {t("workspace.lastModified")}
                </button>
              </div>
            )}
          </div>
        </div>

        {collectionDescription && (
          <p className={styles.description}>{collectionDescription}</p>
        )}
      </header>

      {/* Separator after header */}
      <div className={styles.separator} />

      {/* Action buttons */}
      <div className={styles.actions}>
        <button className={styles.actionButton} onClick={handleImportScenes}>
          {importIcon}
          <span>{t("workspace.importScenes")}</span>
          {plusIcon}
        </button>
        <button className={styles.actionButton} onClick={handleCreateScene}>
          {plusIcon}
          <span>{t("workspace.createScene")}</span>
          {plusIcon}
        </button>
      </div>

      {/* Scene grid */}
      <div className={styles.content}>
        {sortedScenes.length === 0 ? (
          <div className={styles.empty}>
            <div className={styles.emptyIllustration}>
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
            <p className={styles.emptyTitle}>
              {t("workspace.emptyCollection")}
            </p>
            <button className={styles.emptyCta} onClick={handleCreateScene}>
              {t("workspace.letsChangeThat")}
            </button>
            <p className={styles.emptyHint}>{t("workspace.dragDropHint")}</p>
          </div>
        ) : (
          <SceneCardGrid
            scenes={sortedScenes}
            onOpenScene={handleOpenScene}
            onDeleteScene={deleteScene}
            onRenameScene={renameScene}
            onDuplicateScene={duplicateScene}
          />
        )}
      </div>
    </div>
  );
};

export default CollectionView;
