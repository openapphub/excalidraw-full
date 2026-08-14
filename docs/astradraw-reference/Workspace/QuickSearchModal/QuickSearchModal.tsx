import React, {
  useEffect,
  useState,
  useCallback,
  useMemo,
  useRef,
  useId,
} from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import { useAtom, useSetAtom, useAtomValue } from "../../../app-jotai";
import {
  globalSearch,
  type GlobalSearchCollectionResult,
  type GlobalSearchSceneResult,
  type GlobalSearchResponse,
} from "../../../auth/workspaceApi";
import {
  quickSearchOpenAtom,
  navigateToCollectionAtom,
  navigateToSceneAtom,
  currentWorkspaceSlugAtom,
  currentWorkspaceAtom,
  workspacesAtom,
  activeCollectionIdAtom,
  currentSceneIdAtom,
  currentSceneTitleAtom,
  isAutoCollabSceneAtom,
} from "../../Settings/settingsState";
import {
  buildCollectionUrl,
  buildSceneUrl,
  buildPrivateUrl,
} from "../../../router";

import styles from "./QuickSearchModal.module.scss";

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

interface SearchResultItem {
  type: "collection" | "scene";
  id: string;
  title: string;
  icon?: string | null;
  isPrivate?: boolean;
  thumbnailUrl?: string | null;
  collectionName?: string;
  workspaceSlug: string;
  workspaceName: string;
  updatedAt: string;
}

// Global cache for search data (user-level, not workspace-level)
let searchDataCache: {
  data: GlobalSearchResponse;
  timestamp: number;
} | null = null;
const CACHE_TTL = 30000; // 30 seconds

export const QuickSearchModal: React.FC = () => {
  const [isOpen, setIsOpen] = useAtom(quickSearchOpenAtom);
  const navigateToCollection = useSetAtom(navigateToCollectionAtom);
  const navigateToScene = useSetAtom(navigateToSceneAtom);
  const setCurrentWorkspaceSlug = useSetAtom(currentWorkspaceSlugAtom);
  const setCurrentWorkspace = useSetAtom(currentWorkspaceAtom);
  const workspaces = useAtomValue(workspacesAtom);
  const currentWorkspace = useAtomValue(currentWorkspaceAtom);
  const setActiveCollectionId = useSetAtom(activeCollectionIdAtom);
  const setCurrentSceneId = useSetAtom(currentSceneIdAtom);
  const setCurrentSceneTitle = useSetAtom(currentSceneTitleAtom);
  const setIsAutoCollabScene = useSetAtom(isAutoCollabSceneAtom);

  const [query, setQuery] = useState("");
  const [collections, setCollections] = useState<
    GlobalSearchCollectionResult[]
  >([]);
  const [scenes, setScenes] = useState<GlobalSearchSceneResult[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [selectedIndex, setSelectedIndex] = useState(0);

  const searchInputId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const resultsRef = useRef<HTMLDivElement>(null);

  // Load data when modal opens (with caching)
  useEffect(() => {
    if (!isOpen) {
      return;
    }

    const loadData = async () => {
      // Check cache first
      if (
        searchDataCache &&
        Date.now() - searchDataCache.timestamp < CACHE_TTL
      ) {
        setCollections(searchDataCache.data.collections);
        setScenes(searchDataCache.data.scenes);
        return;
      }

      setIsLoading(true);
      try {
        const data = await globalSearch();
        setCollections(data.collections);
        setScenes(data.scenes);

        // Update cache
        searchDataCache = {
          data,
          timestamp: Date.now(),
        };
      } catch (err) {
        console.error("Failed to load search data:", err);
      } finally {
        setIsLoading(false);
      }
    };

    loadData();
    // Focus input when modal opens
    setTimeout(() => inputRef.current?.focus(), 50);
  }, [isOpen]);

  // Reset query and selection when modal closes
  useEffect(() => {
    if (!isOpen) {
      setQuery("");
      setSelectedIndex(0);
    }
  }, [isOpen]);

  // Filter results based on query - only show results when there's a query
  const filteredCollections = useMemo(() => {
    if (!query.trim()) {
      return []; // Don't show anything without a query
    }
    const lowerQuery = query.toLowerCase();
    return collections.filter(
      (c) =>
        c.name.toLowerCase().includes(lowerQuery) ||
        c.workspaceName.toLowerCase().includes(lowerQuery),
    );
  }, [collections, query]);

  const filteredScenes = useMemo(() => {
    if (!query.trim()) {
      return []; // Don't show anything without a query
    }
    const lowerQuery = query.toLowerCase();
    return scenes.filter(
      (s) =>
        s.title.toLowerCase().includes(lowerQuery) ||
        s.workspaceName.toLowerCase().includes(lowerQuery),
    );
  }, [scenes, query]);

  // Build flat list of results for keyboard navigation
  const allResults = useMemo((): SearchResultItem[] => {
    const results: SearchResultItem[] = [];

    // Add collections first
    filteredCollections.forEach((c) => {
      results.push({
        type: "collection",
        id: c.id,
        title: c.isPrivate ? t("workspace.private") : c.name,
        icon: c.icon,
        isPrivate: c.isPrivate,
        workspaceSlug: c.workspaceSlug,
        workspaceName: c.workspaceName,
        updatedAt: c.updatedAt,
      });
    });

    // Add scenes
    filteredScenes.forEach((s) => {
      results.push({
        type: "scene",
        id: s.id,
        title: s.title,
        thumbnailUrl: s.thumbnailUrl,
        collectionName: s.isPrivate
          ? t("workspace.private")
          : s.collectionName ?? undefined,
        isPrivate: s.isPrivate,
        workspaceSlug: s.workspaceSlug,
        workspaceName: s.workspaceName,
        updatedAt: s.updatedAt,
      });
    });

    return results;
  }, [filteredCollections, filteredScenes]);

  // Reset selection when results change, auto-select first result
  useEffect(() => {
    setSelectedIndex(0);
  }, [allResults.length, query]);

  // Scroll selected item into view
  useEffect(() => {
    if (resultsRef.current && allResults.length > 0) {
      const selectedElement = resultsRef.current.querySelector(
        `[data-index="${selectedIndex}"]`,
      );
      if (selectedElement) {
        selectedElement.scrollIntoView({ block: "nearest" });
      }
    }
  }, [selectedIndex, allResults.length]);

  // Handle item selection
  const handleSelect = useCallback(
    (item: SearchResultItem, openInNewTab: boolean = false) => {
      const targetSlug = item.workspaceSlug;
      const isSwitchingWorkspace = currentWorkspace?.slug !== targetSlug;

      if (openInNewTab) {
        // Open in new tab
        let url: string;
        if (item.type === "collection") {
          url = item.isPrivate
            ? buildPrivateUrl(targetSlug)
            : buildCollectionUrl(targetSlug, item.id);
        } else {
          url = buildSceneUrl(targetSlug, item.id);
        }
        window.open(url, "_blank");
      } else {
        // If switching to a different workspace, we need to properly switch context
        if (isSwitchingWorkspace) {
          const targetWorkspace = workspaces.find((w) => w.slug === targetSlug);
          if (targetWorkspace) {
            // Clear current scene state before switching
            setCurrentSceneId(null);
            setCurrentSceneTitle("Untitled");
            setIsAutoCollabScene(false);
            setActiveCollectionId(null);

            // Switch workspace (updates both atom and slug)
            setCurrentWorkspace(targetWorkspace);
            setCurrentWorkspaceSlug(targetSlug);
          }
        }

        if (item.type === "collection") {
          // Navigate to collection in current tab
          navigateToCollection({
            collectionId: item.id,
            isPrivate: item.isPrivate,
          });
        } else {
          // Navigate to scene in current tab
          navigateToScene({
            sceneId: item.id,
            title: item.title,
            workspaceSlug: targetSlug,
          });
        }
      }

      setIsOpen(false);
    },
    [
      navigateToCollection,
      navigateToScene,
      setIsOpen,
      setCurrentWorkspaceSlug,
      currentWorkspace?.slug,
      workspaces,
      setCurrentWorkspace,
      setActiveCollectionId,
      setCurrentSceneId,
      setCurrentSceneTitle,
      setIsAutoCollabScene,
    ],
  );

  // Handle keyboard navigation - this is called from the input's onKeyDown
  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      // Always stop propagation to prevent Excalidraw from handling these keys
      e.stopPropagation();

      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          if (allResults.length > 0) {
            setSelectedIndex((prev) =>
              prev < allResults.length - 1 ? prev + 1 : 0,
            );
          }
          break;
        case "ArrowUp":
          e.preventDefault();
          if (allResults.length > 0) {
            setSelectedIndex((prev) =>
              prev > 0 ? prev - 1 : allResults.length - 1,
            );
          }
          break;
        case "Enter":
          e.preventDefault();
          if (allResults.length > 0 && allResults[selectedIndex]) {
            const openInNewTab = e.metaKey || e.ctrlKey;
            handleSelect(allResults[selectedIndex], openInNewTab);
          }
          break;
        case "Escape":
          e.preventDefault();
          setIsOpen(false);
          break;
      }
    },
    [allResults, selectedIndex, handleSelect, setIsOpen],
  );

  // Handle overlay click
  const handleOverlayClick = useCallback(
    (e: React.MouseEvent) => {
      if (e.target === e.currentTarget) {
        setIsOpen(false);
      }
    },
    [setIsOpen],
  );

  // Format relative time
  const formatRelativeTime = (dateString: string): string => {
    const date = new Date(dateString);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    const diffHours = Math.floor(diffMs / 3600000);
    const diffDays = Math.floor(diffMs / 86400000);

    if (diffMins < 1) {
      return t("workspace.justNow");
    }
    if (diffMins < 60) {
      return t("workspace.minutesAgo", { count: diffMins });
    }
    if (diffHours < 24) {
      return t("workspace.hoursAgo", { count: diffHours });
    }
    if (diffDays === 1) {
      return t("workspace.yesterday");
    }
    if (diffDays < 7) {
      return t("workspace.daysAgo", { count: diffDays });
    }
    return date.toLocaleDateString();
  };

  // Get the index offset for scenes (after collections)
  const scenesStartIndex = filteredCollections.length;

  // Check if we have a query
  const hasQuery = query.trim().length > 0;

  if (!isOpen) {
    return null;
  }

  return (
    <div className={styles.overlay} onClick={handleOverlayClick}>
      <div className={styles.modal}>
        {/* Search input */}
        <div className={styles.inputWrapper}>
          <span className={styles.inputIcon}>{searchIcon}</span>
          <input
            id={searchInputId}
            name="quickSearch"
            ref={inputRef}
            type="text"
            className={styles.input}
            placeholder={t("workspace.quickSearchPlaceholder")}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            onKeyUp={(e) => e.stopPropagation()}
          />
          <button
            className={styles.close}
            onClick={() => setIsOpen(false)}
            aria-label={t("workspace.close")}
          >
            {closeIcon}
          </button>
        </div>

        {/* Keyboard hints */}
        <div className={styles.hints}>
          <span className={styles.hint}>
            <kbd>↑↓</kbd> {t("workspace.select")}
          </span>
          <span className={styles.hint}>
            <kbd>↵</kbd> {t("workspace.open")}
          </span>
          <span className={styles.hint}>
            <kbd>⌘</kbd>+<kbd>↵</kbd> {t("workspace.openInNewTab")}
          </span>
          <span className={styles.hint}>
            <kbd>esc</kbd> {t("workspace.close")}
          </span>
        </div>

        {/* Results */}
        <div className={styles.results} ref={resultsRef}>
          {isLoading ? (
            <div className={styles.loading}>
              <div className={styles.spinner} />
            </div>
          ) : !hasQuery ? (
            // Show prompt when no query
            <div className={styles.empty}>
              <p>{t("workspace.quickSearchPrompt")}</p>
            </div>
          ) : allResults.length === 0 ? (
            // Show no results message
            <div className={styles.empty}>
              <p>{t("workspace.noSearchResults")}</p>
              <span>{t("workspace.noSearchResultsHint")}</span>
            </div>
          ) : (
            <>
              {/* Collections section */}
              {filteredCollections.length > 0 && (
                <>
                  <div className={styles.sectionHeader}>
                    {t("workspace.collections")}
                  </div>
                  {filteredCollections.map((collection, index) => (
                    <button
                      key={`${collection.workspaceId}-${collection.id}`}
                      data-index={index}
                      className={`${styles.item} ${
                        selectedIndex === index ? styles.itemSelected : ""
                      }`}
                      onClick={() =>
                        handleSelect(
                          {
                            type: "collection",
                            id: collection.id,
                            title: collection.isPrivate
                              ? t("workspace.private")
                              : collection.name,
                            isPrivate: collection.isPrivate,
                            icon: collection.icon,
                            workspaceSlug: collection.workspaceSlug,
                            workspaceName: collection.workspaceName,
                            updatedAt: collection.updatedAt,
                          },
                          false,
                        )
                      }
                      onMouseEnter={() => setSelectedIndex(index)}
                    >
                      <span className={styles.itemIcon}>
                        {collection.isPrivate ? (
                          lockIcon
                        ) : collection.icon ? (
                          collection.icon
                        ) : (
                          <span className={styles.itemIconDefault}>
                            {folderIcon}
                          </span>
                        )}
                      </span>
                      <div className={styles.itemContent}>
                        <span className={styles.itemTitle}>
                          {collection.isPrivate
                            ? t("workspace.private")
                            : collection.name}
                        </span>
                        <span className={styles.itemMeta}>
                          {formatRelativeTime(collection.updatedAt)}
                          {" • "}
                          <span className={styles.itemWorkspace}>
                            {collection.workspaceName}
                          </span>
                        </span>
                      </div>
                    </button>
                  ))}
                </>
              )}

              {/* Divider between sections */}
              {filteredCollections.length > 0 && filteredScenes.length > 0 && (
                <div className={styles.divider} />
              )}

              {/* Scenes section */}
              {filteredScenes.length > 0 && (
                <>
                  <div className={styles.sectionHeader}>
                    {t("workspace.scenes")}
                  </div>
                  {filteredScenes.map((scene, index) => {
                    const actualIndex = scenesStartIndex + index;
                    return (
                      <button
                        key={`${scene.workspaceId}-${scene.id}`}
                        data-index={actualIndex}
                        className={`${styles.item} ${
                          selectedIndex === actualIndex
                            ? styles.itemSelected
                            : ""
                        }`}
                        onClick={() =>
                          handleSelect(
                            {
                              type: "scene",
                              id: scene.id,
                              title: scene.title,
                              thumbnailUrl: scene.thumbnailUrl,
                              collectionName: scene.isPrivate
                                ? t("workspace.private")
                                : scene.collectionName ?? undefined,
                              isPrivate: scene.isPrivate,
                              workspaceSlug: scene.workspaceSlug,
                              workspaceName: scene.workspaceName,
                              updatedAt: scene.updatedAt,
                            },
                            false,
                          )
                        }
                        onMouseEnter={() => setSelectedIndex(actualIndex)}
                      >
                        <span className={styles.itemThumbnail}>
                          {scene.thumbnailUrl ? (
                            <img src={scene.thumbnailUrl} alt="" />
                          ) : (
                            <span className={styles.itemThumbnailPlaceholder} />
                          )}
                        </span>
                        <div className={styles.itemContent}>
                          <span className={styles.itemTitle}>
                            {scene.title}
                          </span>
                          <span className={styles.itemMeta}>
                            {formatRelativeTime(scene.updatedAt)}
                            {scene.collectionName && (
                              <>
                                {" • "}
                                <span className={styles.itemCollection}>
                                  {t("workspace.inCollection")}{" "}
                                  {scene.isPrivate
                                    ? t("workspace.private")
                                    : scene.collectionName}
                                </span>
                              </>
                            )}
                            {" • "}
                            <span className={styles.itemWorkspace}>
                              {scene.workspaceName}
                            </span>
                          </span>
                        </div>
                        {scene.isPrivate && (
                          <span className={styles.itemBadge}>{lockIcon}</span>
                        )}
                      </button>
                    );
                  })}
                </>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
};

export default QuickSearchModal;
