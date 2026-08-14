import clsx from "clsx";
import { useCallback, useEffect, useState, useRef } from "react";

import { PlusIcon } from "@excalidraw/excalidraw/components/icons";
import { t } from "@excalidraw/excalidraw/i18n";
import { exportToCanvas } from "@excalidraw/utils";

import type { ExcalidrawFrameLikeElement } from "@excalidraw/element/types";
import type {
  ExcalidrawImperativeAPI,
  BinaryFiles,
} from "@excalidraw/excalidraw/types";

import { usePresentationMode } from "../usePresentationMode";
import { SlidesLayoutDialog, applyLayoutToFrames } from "../SlidesLayoutDialog";

import styles from "./PresentationPanel.module.scss";

import type { LayoutType } from "../SlidesLayoutDialog";

// Helper to extract the order prefix from a frame name (e.g., "3. My Frame" -> 3)
const extractOrderPrefix = (name: string | null): number | null => {
  if (!name) {
    return null;
  }
  const match = name.match(/^(\d+)\.\s*/);
  return match ? parseInt(match[1], 10) : null;
};

// Helper to remove the order prefix from a frame name (e.g., "3. My Frame" -> "My Frame")
const removeOrderPrefix = (name: string | null): string => {
  if (!name) {
    return "";
  }
  return name.replace(/^\d+\.\s*/, "");
};

// Helper to add/update order prefix to a frame name
const setOrderPrefix = (name: string | null, order: number): string => {
  const baseName = removeOrderPrefix(name) || `Frame`;
  return `${order}. ${baseName}`;
};

interface PresentationPanelProps {
  excalidrawAPI: ExcalidrawImperativeAPI | null;
}

interface SlideThumbProps {
  frame: ExcalidrawFrameLikeElement;
  index: number;
  isActive: boolean;
  isDragging: boolean;
  isDragOver: boolean;
  onClick: () => void;
  onRename: (newName: string) => void;
  onDragStart: (e: React.DragEvent) => void;
  onDragEnd: (e: React.DragEvent) => void;
  onDragOver: (e: React.DragEvent) => void;
  onDragLeave: (e: React.DragEvent) => void;
  onDrop: (e: React.DragEvent) => void;
  excalidrawAPI: ExcalidrawImperativeAPI | null;
  refreshKey?: number;
}

const SlideThumb: React.FC<SlideThumbProps> = ({
  frame,
  index,
  isActive,
  isDragging,
  isDragOver,
  onClick,
  onRename,
  onDragStart,
  onDragEnd,
  onDragOver,
  onDragLeave,
  onDrop,
  excalidrawAPI,
  refreshKey,
}) => {
  const frameName = frame.name || `Frame ${index + 1}`;
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const [previewError, setPreviewError] = useState(false);
  const [isEditing, setIsEditing] = useState(false);
  const [editValue, setEditValue] = useState(frameName);

  // Update editValue when frame name changes externally
  useEffect(() => {
    if (!isEditing) {
      setEditValue(frame.name || `Frame ${index + 1}`);
    }
  }, [frame.name, index, isEditing]);

  // Focus input when entering edit mode
  useEffect(() => {
    if (isEditing && inputRef.current) {
      inputRef.current.focus();
      inputRef.current.select();
    }
  }, [isEditing]);

  const handleStartEditing = (e: React.MouseEvent) => {
    e.stopPropagation();
    e.preventDefault();
    setIsEditing(true);
    setEditValue(frame.name || "");
  };

  const handleFinishEditing = () => {
    setIsEditing(false);
    const trimmedValue = editValue.trim();
    if (trimmedValue !== frame.name) {
      onRename(trimmedValue);
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      handleFinishEditing();
    } else if (e.key === "Escape") {
      setIsEditing(false);
      setEditValue(frame.name || `Frame ${index + 1}`);
    }
  };

  // Generate preview when frame changes
  useEffect(() => {
    if (!excalidrawAPI || !canvasRef.current) {
      return;
    }

    const generatePreview = async () => {
      try {
        const elements = excalidrawAPI.getSceneElements();
        const appState = excalidrawAPI.getAppState();
        const files = excalidrawAPI.getFiles();

        // Export the frame content to canvas
        const exportedCanvas = await exportToCanvas({
          elements,
          appState: {
            ...appState,
            exportBackground: true,
            viewBackgroundColor: appState.viewBackgroundColor,
          },
          files: files as BinaryFiles,
          exportingFrame: frame,
          maxWidthOrHeight: 300, // Larger preview for better quality
        });

        // Draw the exported canvas onto our preview canvas
        const ctx = canvasRef.current?.getContext("2d");
        if (ctx && canvasRef.current) {
          const previewWidth = canvasRef.current.width;
          const previewHeight = canvasRef.current.height;

          // Clear canvas
          ctx.clearRect(0, 0, previewWidth, previewHeight);

          // Calculate scaling to fit while maintaining aspect ratio
          const scale = Math.min(
            previewWidth / exportedCanvas.width,
            previewHeight / exportedCanvas.height,
          );

          const scaledWidth = exportedCanvas.width * scale;
          const scaledHeight = exportedCanvas.height * scale;

          // Center the preview
          const offsetX = (previewWidth - scaledWidth) / 2;
          const offsetY = (previewHeight - scaledHeight) / 2;

          ctx.drawImage(
            exportedCanvas,
            offsetX,
            offsetY,
            scaledWidth,
            scaledHeight,
          );
          setPreviewError(false);
        }
      } catch (error) {
        console.error("Failed to generate frame preview:", error);
        setPreviewError(true);
      }
    };

    // Debounce preview generation
    const timeoutId = setTimeout(generatePreview, 100);
    return () => clearTimeout(timeoutId);
  }, [frame, excalidrawAPI, refreshKey]);

  return (
    <div
      className={clsx(styles.slide, {
        [styles.slideActive]: isActive,
        [styles.slideDragging]: isDragging,
        [styles.slideDragOver]: isDragOver,
      })}
      draggable
      onDragStart={onDragStart}
      onDragEnd={onDragEnd}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      {/* Drop indicator line */}
      {isDragOver && <div className={styles.dropIndicator} />}

      <div
        className={styles.slideContent}
        onClick={onClick}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            onClick();
          }
        }}
        title={`${t("presentation.goToSlide")} ${frameName}`}
      >
        <div className={styles.slidePreview}>
          <canvas
            ref={canvasRef}
            width={280}
            height={158}
            className={styles.slideCanvas}
          />
          {previewError && (
            <span className={styles.slideNumber}>{index + 1}</span>
          )}
          {/* Hover overlay with actions */}
          <div className={styles.slideOverlay}>
            <button
              className={styles.overlayAction}
              onClick={handleStartEditing}
              title={t("presentation.renameFrame")}
            >
              <svg
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
              >
                <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7" />
                <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z" />
              </svg>
            </button>
            <div
              className={styles.overlayDragHandle}
              title={t("presentation.dragToReorder")}
            >
              <svg
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="currentColor"
              >
                <circle cx="9" cy="5" r="1.5" />
                <circle cx="15" cy="5" r="1.5" />
                <circle cx="9" cy="12" r="1.5" />
                <circle cx="15" cy="12" r="1.5" />
                <circle cx="9" cy="19" r="1.5" />
                <circle cx="15" cy="19" r="1.5" />
              </svg>
            </div>
          </div>
        </div>
      </div>

      {/* Frame name below preview */}
      {isEditing ? (
        <input
          ref={inputRef}
          type="text"
          className={styles.slideNameInput}
          value={editValue}
          onChange={(e) => setEditValue(e.target.value)}
          onBlur={handleFinishEditing}
          onKeyDown={handleKeyDown}
          onClick={(e) => e.stopPropagation()}
          placeholder={`Frame ${index + 1}`}
        />
      ) : (
        <span
          className={styles.slideName}
          onDoubleClick={handleStartEditing}
          title={t("presentation.doubleClickToRename")}
        >
          {frameName}
        </span>
      )}
    </div>
  );
};

const PresentationInstructions: React.FC<{
  onCreateSlide: () => void;
}> = ({ onCreateSlide }) => {
  return (
    <div className={styles.instructions}>
      <div className={styles.instructionsIcon}>
        <svg
          width="64"
          height="64"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
          strokeLinejoin="round"
        >
          <path d="M3 4l18 0" />
          <path d="M4 4v10a2 2 0 0 0 2 2h12a2 2 0 0 0 2 -2v-10" />
          <path d="M12 16l0 4" />
          <path d="M9 20l6 0" />
          <path d="M8 12l3 -3l2 2l3 -3" />
        </svg>
      </div>
      <h3 className={styles.instructionsTitle}>
        {t("presentation.noFramesTitle")}
      </h3>
      <p className={styles.instructionsText}>
        {t("presentation.noFramesDescription")}
      </p>
      <ol className={styles.instructionsSteps}>
        <li>{t("presentation.noFramesStep1")}</li>
        <li>{t("presentation.noFramesStep2")}</li>
        <li>{t("presentation.noFramesStep3")}</li>
        <li>{t("presentation.noFramesStep4")}</li>
      </ol>
      <button className={styles.createSlideButton} onClick={onCreateSlide}>
        {PlusIcon}
        <span>{t("presentation.createFirstSlide")}</span>
      </button>
    </div>
  );
};

export const PresentationPanel: React.FC<PresentationPanelProps> = ({
  excalidrawAPI,
}) => {
  // Local state for ordered frames (allows reordering)
  const [orderedFrames, setOrderedFrames] = useState<
    ExcalidrawFrameLikeElement[]
  >([]);
  const [selectedFrameIndex, setSelectedFrameIndex] = useState<number | null>(
    null,
  );
  // Key to trigger preview refresh when scene changes
  const [previewRefreshKey, setPreviewRefreshKey] = useState(0);

  // Layout dialog state
  const [isLayoutDialogOpen, setIsLayoutDialogOpen] = useState(false);

  // Drag and drop state
  const [draggedIndex, setDraggedIndex] = useState<number | null>(null);
  const [dragOverIndex, setDragOverIndex] = useState<number | null>(null);
  const slidesContainerRef = useRef<HTMLDivElement>(null);
  const autoScrollIntervalRef = useRef<number | null>(null);

  const { startPresentation, getFrames, isPresentationMode, setSlides } =
    usePresentationMode({
      excalidrawAPI,
    });

  // Refresh frames list - sort by order prefix if present
  const refreshFrames = useCallback(() => {
    const currentFrames = getFrames();

    // Sort frames by their order prefix (if any), then by name
    const sortedFrames = [...currentFrames].sort((a, b) => {
      const orderA = extractOrderPrefix(a.name);
      const orderB = extractOrderPrefix(b.name);

      // Both have order prefixes - sort by prefix
      if (orderA !== null && orderB !== null) {
        return orderA - orderB;
      }
      // Only one has prefix - prefixed comes first
      if (orderA !== null) {
        return -1;
      }
      if (orderB !== null) {
        return 1;
      }
      // Neither has prefix - sort by name
      const nameA = a.name || "";
      const nameB = b.name || "";
      return nameA.localeCompare(nameB);
    });

    setOrderedFrames((prevOrdered) => {
      if (prevOrdered.length === 0) {
        return sortedFrames;
      }

      // Keep existing order, add new frames at the end, remove deleted frames
      const existingIds = new Set(currentFrames.map((f) => f.id));
      const orderedIds = new Set(prevOrdered.map((f) => f.id));

      // Keep frames that still exist in their current order
      const kept = prevOrdered.filter((f) => existingIds.has(f.id));

      // Add new frames at the end (sorted by their prefix if they have one)
      const newFrames = sortedFrames.filter((f) => !orderedIds.has(f.id));

      // Update references to current frame objects
      const updatedKept = kept.map(
        (f) => currentFrames.find((cf) => cf.id === f.id) || f,
      );

      return [...updatedKept, ...newFrames];
    });

    // Trigger preview refresh
    setPreviewRefreshKey((prev) => prev + 1);
  }, [getFrames]);

  // Refresh on mount and when elements change
  useEffect(() => {
    refreshFrames();

    if (!excalidrawAPI) {
      return;
    }

    // Subscribe to changes
    const unsubscribe = excalidrawAPI.onChange(() => {
      refreshFrames();
    });

    return () => unsubscribe();
  }, [excalidrawAPI, refreshFrames]);

  // Handle create slide button
  const handleCreateSlide = useCallback(() => {
    if (!excalidrawAPI) {
      return;
    }

    excalidrawAPI.setActiveTool({ type: "frame" });
    excalidrawAPI.setToast({
      message: t("presentation.drawFrameToast"),
      duration: 3000,
      closable: true,
    });
  }, [excalidrawAPI]);

  // Handle slide click - scroll to frame
  const handleSlideClick = useCallback(
    (index: number) => {
      if (!excalidrawAPI || !orderedFrames[index]) {
        return;
      }

      setSelectedFrameIndex(index);
      excalidrawAPI.scrollToContent(orderedFrames[index], {
        fitToContent: true,
        animate: true,
        duration: 300,
      });
    },
    [excalidrawAPI, orderedFrames],
  );

  // Helper to update frame names with order prefixes after reordering
  const updateFrameOrderPrefixes = useCallback(
    (frames: ExcalidrawFrameLikeElement[]) => {
      if (!excalidrawAPI) {
        return;
      }

      const elements = excalidrawAPI.getSceneElements();
      const updatedElements = elements.map((el) => {
        if (el.type === "frame" || el.type === "magicframe") {
          const frameIndex = frames.findIndex((f) => f.id === el.id);
          if (frameIndex !== -1) {
            const newName = setOrderPrefix(el.name, frameIndex + 1);
            if (newName !== el.name) {
              return { ...el, name: newName };
            }
          }
        }
        return el;
      });

      excalidrawAPI.updateScene({
        elements: updatedElements,
      });
    },
    [excalidrawAPI],
  );

  // Drag and drop handlers
  const handleDragStart = useCallback(
    (index: number) => (e: React.DragEvent) => {
      setDraggedIndex(index);
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/plain", String(index));

      // Add a slight delay to allow the drag image to be created
      requestAnimationFrame(() => {
        const target = e.target as HTMLElement;
        target.style.opacity = "0.5";
      });
    },
    [],
  );

  const handleDragEnd = useCallback(
    () => (e: React.DragEvent) => {
      const target = e.target as HTMLElement;
      target.style.opacity = "";
      setDraggedIndex(null);
      setDragOverIndex(null);

      // Clear auto-scroll interval
      if (autoScrollIntervalRef.current) {
        clearInterval(autoScrollIntervalRef.current);
        autoScrollIntervalRef.current = null;
      }
    },
    [],
  );

  const handleDragOver = useCallback(
    (index: number) => (e: React.DragEvent) => {
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";

      if (draggedIndex !== null && draggedIndex !== index) {
        setDragOverIndex(index);
      }

      // Auto-scroll when near edges
      const container = slidesContainerRef.current;
      if (container) {
        const rect = container.getBoundingClientRect();
        const scrollZone = 60;

        // Clear existing interval
        if (autoScrollIntervalRef.current) {
          clearInterval(autoScrollIntervalRef.current);
          autoScrollIntervalRef.current = null;
        }

        if (e.clientY < rect.top + scrollZone) {
          // Scroll up
          autoScrollIntervalRef.current = window.setInterval(() => {
            container.scrollBy(0, -8);
          }, 16);
        } else if (e.clientY > rect.bottom - scrollZone) {
          // Scroll down
          autoScrollIntervalRef.current = window.setInterval(() => {
            container.scrollBy(0, 8);
          }, 16);
        }
      }
    },
    [draggedIndex],
  );

  const handleDragLeave = useCallback(
    () => (e: React.DragEvent) => {
      // Only clear if we're leaving the slide entirely
      const relatedTarget = e.relatedTarget as HTMLElement;
      const currentTarget = e.currentTarget as HTMLElement;
      if (!currentTarget.contains(relatedTarget)) {
        setDragOverIndex(null);
      }
    },
    [],
  );

  const handleDrop = useCallback(
    (dropIndex: number) => (e: React.DragEvent) => {
      e.preventDefault();

      if (draggedIndex === null || draggedIndex === dropIndex) {
        setDraggedIndex(null);
        setDragOverIndex(null);
        return;
      }

      // Reorder the frames
      setOrderedFrames((prev) => {
        const newOrder = [...prev];
        const [draggedItem] = newOrder.splice(draggedIndex, 1);

        // Adjust drop index if dragging from before to after
        const adjustedDropIndex =
          draggedIndex < dropIndex ? dropIndex - 1 : dropIndex;
        newOrder.splice(adjustedDropIndex, 0, draggedItem);

        // Update frame names with new order prefixes
        updateFrameOrderPrefixes(newOrder);

        return newOrder;
      });

      setDraggedIndex(null);
      setDragOverIndex(null);

      // Clear auto-scroll interval
      if (autoScrollIntervalRef.current) {
        clearInterval(autoScrollIntervalRef.current);
        autoScrollIntervalRef.current = null;
      }
    },
    [draggedIndex, updateFrameOrderPrefixes],
  );

  // Handle rename frame
  const handleRenameFrame = useCallback(
    (frameId: string, newName: string) => {
      if (!excalidrawAPI) {
        return;
      }

      const elements = excalidrawAPI.getSceneElements();
      const updatedElements = elements.map((el) => {
        if (
          el.id === frameId &&
          (el.type === "frame" || el.type === "magicframe")
        ) {
          return {
            ...el,
            name: newName || null,
          };
        }
        return el;
      });

      excalidrawAPI.updateScene({
        elements: updatedElements,
      });
    },
    [excalidrawAPI],
  );

  // Handle start presentation - use ordered frames
  const handleStartPresentation = useCallback(() => {
    // Set the ordered slides before starting
    setSlides(orderedFrames);
    startPresentation();
  }, [startPresentation, setSlides, orderedFrames]);

  // Handle layout apply
  const handleApplyLayout = useCallback(
    (layout: LayoutType, columnCount: number) => {
      if (!excalidrawAPI || orderedFrames.length === 0) {
        return;
      }
      applyLayoutToFrames(excalidrawAPI, orderedFrames, layout, columnCount);
    },
    [excalidrawAPI, orderedFrames],
  );

  // Cleanup auto-scroll on unmount
  useEffect(() => {
    return () => {
      if (autoScrollIntervalRef.current) {
        clearInterval(autoScrollIntervalRef.current);
      }
    };
  }, []);

  // Don't render if in presentation mode
  if (isPresentationMode) {
    return null;
  }

  const hasFrames = orderedFrames.length > 0;

  return (
    <div className={styles.panel} onWheel={(e) => e.stopPropagation()}>
      {hasFrames ? (
        <>
          {/* Header */}
          <div className={styles.header}>
            <span className={styles.title}>{t("presentation.title")}</span>
            <div className={styles.headerActions}>
              <button
                className={styles.headerButton}
                onClick={() => setIsLayoutDialogOpen(true)}
                title={t("slidesLayout.title")}
              >
                <svg
                  width="18"
                  height="18"
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                >
                  <rect x="3" y="3" width="7" height="7" rx="1" />
                  <rect x="14" y="3" width="7" height="7" rx="1" />
                  <rect x="3" y="14" width="7" height="7" rx="1" />
                  <rect x="14" y="14" width="7" height="7" rx="1" />
                </svg>
              </button>
              <button
                className={styles.headerButton}
                onClick={handleCreateSlide}
                title={t("presentation.createSlide")}
              >
                {PlusIcon}
              </button>
            </div>
          </div>

          {/* Layout Dialog */}
          <SlidesLayoutDialog
            isOpen={isLayoutDialogOpen}
            onClose={() => setIsLayoutDialogOpen(false)}
            onApply={handleApplyLayout}
          />

          {/* Slides list */}
          <div
            className={styles.slides}
            ref={slidesContainerRef}
            onWheel={(e) => e.stopPropagation()}
          >
            {orderedFrames.map((frame, index) => (
              <SlideThumb
                key={frame.id}
                frame={frame}
                index={index}
                isActive={selectedFrameIndex === index}
                isDragging={draggedIndex === index}
                isDragOver={dragOverIndex === index}
                onClick={() => handleSlideClick(index)}
                onRename={(newName) => handleRenameFrame(frame.id, newName)}
                onDragStart={handleDragStart(index)}
                onDragEnd={handleDragEnd()}
                onDragOver={handleDragOver(index)}
                onDragLeave={handleDragLeave()}
                onDrop={handleDrop(index)}
                excalidrawAPI={excalidrawAPI}
                refreshKey={previewRefreshKey}
              />
            ))}
          </div>

          {/* Start button */}
          <div className={styles.footer}>
            <button
              className={styles.startButton}
              onClick={handleStartPresentation}
            >
              <svg
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="currentColor"
              >
                <path d="M8 5v14l11-7z" />
              </svg>
              <span>{t("presentation.startPresentation")}</span>
            </button>
          </div>
        </>
      ) : (
        <PresentationInstructions onCreateSlide={handleCreateSlide} />
      )}
    </div>
  );
};
