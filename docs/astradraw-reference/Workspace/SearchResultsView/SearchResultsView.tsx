import React, { useCallback, useMemo } from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import { useAtom, useAtomValue, useSetAtom } from "../../../app-jotai";
import { type WorkspaceScene } from "../../../auth/workspaceApi";
import { useScenesCache } from "../../../hooks/useScenesCache";
import {
  searchQueryAtom,
  navigateToSceneAtom,
  currentWorkspaceSlugAtom,
  currentWorkspaceAtom,
} from "../../Settings/settingsState";
import { useSceneActions } from "../../../hooks/useSceneActions";

import { SceneCardGrid } from "../SceneCardGrid";

import styles from "./SearchResultsView.module.scss";

// Icons
const searchIcon = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
    <circle cx="11" cy="11" r="8" />
    <path d="M21 21l-4.35-4.35" />
  </svg>
);

const closeIcon = (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
    <path d="M18 6L6 18M6 6l12 12" />
  </svg>
);

export const SearchResultsView: React.FC = () => {
  // Read workspace from Jotai atom
  const workspace = useAtomValue(currentWorkspaceAtom);
  const [searchQuery, setSearchQuery] = useAtom(searchQueryAtom);
  const navigateToScene = useSetAtom(navigateToSceneAtom);
  const workspaceSlug = useAtomValue(currentWorkspaceSlugAtom);

  // Use React Query for scenes - fetch all scenes (no collection filter)
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

  // Filter scenes by search query
  const filteredScenes = useMemo(() => {
    if (!searchQuery.trim()) {
      return allScenes;
    }
    const lowerQuery = searchQuery.toLowerCase();
    return allScenes.filter((scene) =>
      scene.title.toLowerCase().includes(lowerQuery),
    );
  }, [allScenes, searchQuery]);

  // Navigate to scene via URL
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

  const handleClearSearch = useCallback(() => {
    setSearchQuery("");
  }, [setSearchQuery]);

  if (isLoading) {
    return (
      <div className={styles.view}>
        <div className={styles.loading}>
          <div className={styles.spinner} />
        </div>
      </div>
    );
  }

  return (
    <div className={styles.view}>
      {/* Header */}
      <header className={styles.header}>
        <div className={styles.titleRow}>
          <span className={styles.icon}>{searchIcon}</span>
          <h1 className={styles.title}>
            {searchQuery.trim()
              ? t("workspace.searchResultsFor", { query: searchQuery })
              : t("workspace.searchResults")}
          </h1>
          <button
            className={styles.clear}
            onClick={handleClearSearch}
            title={t("workspace.clearSearch")}
          >
            {closeIcon}
            <span>{t("workspace.clearSearch")}</span>
          </button>
        </div>
        <p className={styles.count}>
          {filteredScenes.length === 1
            ? `1 ${t("workspace.scenes").toLowerCase()}`
            : `${filteredScenes.length} ${t("workspace.scenes").toLowerCase()}`}
        </p>
      </header>

      {/* Separator after header */}
      <div className={styles.separator} />

      {/* Results */}
      <div className={styles.content}>
        {filteredScenes.length === 0 ? (
          <div className={styles.empty}>
            <div className={styles.emptyIcon}>{searchIcon}</div>
            <p className={styles.emptyTitle}>
              {t("workspace.noSearchResults")}
            </p>
            <p className={styles.emptyHint}>
              {t("workspace.noSearchResultsHint")}
            </p>
            <button className={styles.emptyCta} onClick={handleClearSearch}>
              {t("workspace.clearSearch")}
            </button>
          </div>
        ) : (
          <SceneCardGrid
            scenes={filteredScenes}
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

export default SearchResultsView;
