import clsx from "clsx";
import { useState } from "react";

import { Dialog } from "@excalidraw/excalidraw/components/Dialog";
import { t } from "@excalidraw/excalidraw/i18n";

import type { ExcalidrawFrameLikeElement } from "@excalidraw/element/types";
import type { ExcalidrawImperativeAPI } from "@excalidraw/excalidraw/types";

import styles from "./SlidesLayoutDialog.module.scss";

export type LayoutType = "row" | "column" | "grid";

interface SlidesLayoutDialogProps {
  isOpen: boolean;
  onClose: () => void;
  onApply: (layout: LayoutType, columnCount: number) => void;
}

export const SlidesLayoutDialog: React.FC<SlidesLayoutDialogProps> = ({
  isOpen,
  onClose,
  onApply,
}) => {
  const [selectedLayout, setSelectedLayout] = useState<LayoutType>("row");
  const [columnCount, setColumnCount] = useState(3);

  if (!isOpen) {
    return null;
  }

  const handleApply = () => {
    onApply(selectedLayout, columnCount);
    onClose();
  };

  return (
    <Dialog
      onCloseRequest={onClose}
      title={t("slidesLayout.title")}
      size="wide"
    >
      <div className={styles.dialog}>
        <p className={styles.description}>{t("slidesLayout.description")}</p>

        <p className={styles.orderNote}>
          <strong>{t("slidesLayout.orderNote")}</strong>
          <br />
          <span className={styles.orderNoteSub}>
            {t("slidesLayout.orderNoteSub")}
          </span>
        </p>

        <div className={styles.options}>
          {/* Row Layout */}
          <button
            className={clsx(styles.option, {
              [styles.optionSelected]: selectedLayout === "row",
            })}
            onClick={() => setSelectedLayout("row")}
          >
            <div className={styles.optionPreview}>
              <div className={styles.mockWindow}>
                <div className={styles.windowDots}>
                  <span className={clsx(styles.dot, styles.dotRed)} />
                  <span className={clsx(styles.dot, styles.dotYellow)} />
                  <span className={clsx(styles.dot, styles.dotGreen)} />
                </div>
                <div className={clsx(styles.mockFrames, styles.mockFramesRow)}>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(styles.mockShape, styles.mockShapeCircle)}
                    />
                  </div>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(styles.mockShape, styles.mockShapeSquare)}
                    />
                  </div>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(
                        styles.mockShape,
                        styles.mockShapeTriangle,
                      )}
                    />
                  </div>
                </div>
              </div>
            </div>
            <div className={styles.optionLabel}>
              <span className={styles.optionName}>{t("slidesLayout.row")}</span>
              {selectedLayout === "row" && (
                <span className={styles.optionCheck}>✓</span>
              )}
            </div>
          </button>

          {/* Column Layout */}
          <button
            className={clsx(styles.option, {
              [styles.optionSelected]: selectedLayout === "column",
            })}
            onClick={() => setSelectedLayout("column")}
          >
            <div className={styles.optionPreview}>
              <div className={styles.mockWindow}>
                <div className={styles.windowDots}>
                  <span className={clsx(styles.dot, styles.dotRed)} />
                  <span className={clsx(styles.dot, styles.dotYellow)} />
                  <span className={clsx(styles.dot, styles.dotGreen)} />
                </div>
                <div
                  className={clsx(styles.mockFrames, styles.mockFramesColumn)}
                >
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(styles.mockShape, styles.mockShapeCircle)}
                    />
                  </div>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(styles.mockShape, styles.mockShapeSquare)}
                    />
                  </div>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(
                        styles.mockShape,
                        styles.mockShapeTriangle,
                      )}
                    />
                  </div>
                </div>
              </div>
            </div>
            <div className={styles.optionLabel}>
              <span className={styles.optionName}>
                {t("slidesLayout.column")}
              </span>
              {selectedLayout === "column" && (
                <span className={styles.optionCheck}>✓</span>
              )}
            </div>
          </button>

          {/* Grid Layout */}
          <button
            className={clsx(styles.option, {
              [styles.optionSelected]: selectedLayout === "grid",
            })}
            onClick={() => setSelectedLayout("grid")}
          >
            <div className={styles.optionPreview}>
              <div className={styles.mockWindow}>
                <div className={styles.windowDots}>
                  <span className={clsx(styles.dot, styles.dotRed)} />
                  <span className={clsx(styles.dot, styles.dotYellow)} />
                  <span className={clsx(styles.dot, styles.dotGreen)} />
                </div>
                <div className={clsx(styles.mockFrames, styles.mockFramesGrid)}>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(styles.mockShape, styles.mockShapeCircle)}
                    />
                  </div>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(styles.mockShape, styles.mockShapeSquare)}
                    />
                  </div>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(
                        styles.mockShape,
                        styles.mockShapeTriangle,
                      )}
                    />
                  </div>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(styles.mockShape, styles.mockShapeStar)}
                    />
                  </div>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(
                        styles.mockShape,
                        styles.mockShapeDiamond,
                      )}
                    />
                  </div>
                  <div className={styles.mockFrame}>
                    <span
                      className={clsx(
                        styles.mockShape,
                        styles.mockShapeHexagon,
                      )}
                    />
                  </div>
                </div>
              </div>
            </div>
            <div className={styles.optionLabel}>
              <span className={styles.optionName}>
                {t("slidesLayout.grid")}
              </span>
              {selectedLayout === "grid" && (
                <span className={styles.optionCheck}>✓</span>
              )}
            </div>
          </button>
        </div>

        {/* Column count selector for grid */}
        <div className={styles.columnCount}>
          <label className={styles.columnLabel}>
            {t("slidesLayout.columnCount")}
            <span className={styles.columnDescription}>
              {t("slidesLayout.columnCountDescription")}
            </span>
          </label>
          <select
            className={styles.columnSelect}
            value={columnCount}
            onChange={(e) => setColumnCount(Number(e.target.value))}
            disabled={selectedLayout !== "grid"}
          >
            {[2, 3, 4, 5, 6].map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        </div>

        {/* Action buttons */}
        <div className={styles.actions}>
          <button
            className={clsx(styles.button, styles.buttonSecondary)}
            onClick={onClose}
          >
            {t("slidesLayout.close")}
          </button>
          <button
            className={clsx(styles.button, styles.buttonPrimary)}
            onClick={handleApply}
          >
            {t("slidesLayout.apply")}
          </button>
        </div>
      </div>
    </Dialog>
  );
};

// Helper function to apply layout to frames
export const applyLayoutToFrames = (
  excalidrawAPI: ExcalidrawImperativeAPI,
  frames: ExcalidrawFrameLikeElement[],
  layout: LayoutType,
  columnCount: number,
  gap: number = 50,
): void => {
  if (!excalidrawAPI || frames.length === 0) {
    return;
  }

  const elements = excalidrawAPI.getSceneElements();

  // Find the maximum frame dimensions to use as reference
  let maxWidth = 0;
  let maxHeight = 0;
  for (const frame of frames) {
    maxWidth = Math.max(maxWidth, frame.width);
    maxHeight = Math.max(maxHeight, frame.height);
  }

  // Calculate starting position (center of current view or origin)
  const appState = excalidrawAPI.getAppState();
  const startX = appState.scrollX * -1 + 100;
  const startY = appState.scrollY * -1 + 100;

  // Calculate new positions for each frame
  const framePositions: Map<string, { x: number; y: number }> = new Map();

  frames.forEach((frame, index) => {
    let x: number;
    let y: number;

    switch (layout) {
      case "row":
        // Horizontal arrangement
        x = startX + index * (maxWidth + gap);
        y = startY;
        break;
      case "column":
        // Vertical arrangement
        x = startX;
        y = startY + index * (maxHeight + gap);
        break;
      case "grid":
        // Grid arrangement
        const col = index % columnCount;
        const row = Math.floor(index / columnCount);
        x = startX + col * (maxWidth + gap);
        y = startY + row * (maxHeight + gap);
        break;
      default:
        x = frame.x;
        y = frame.y;
    }

    framePositions.set(frame.id, { x, y });
  });

  // Update elements with new positions
  // We need to move frames and all their contained elements
  const updatedElements = elements.map((el) => {
    // Check if this is a frame we're moving
    const newPos = framePositions.get(el.id);
    if (newPos) {
      const frame = frames.find((f) => f.id === el.id);
      if (frame) {
        // Delta values kept for potential future use (e.g., moving child elements)
        const _deltaX = newPos.x - frame.x;
        const _deltaY = newPos.y - frame.y;
        void _deltaX;
        void _deltaY;
        return {
          ...el,
          x: newPos.x,
          y: newPos.y,
        };
      }
    }

    // Check if this element is inside a frame we're moving
    if (el.frameId) {
      const frame = frames.find((f) => f.id === el.frameId);
      if (frame) {
        const newPos = framePositions.get(frame.id);
        if (newPos) {
          const deltaX = newPos.x - frame.x;
          const deltaY = newPos.y - frame.y;
          return {
            ...el,
            x: el.x + deltaX,
            y: el.y + deltaY,
          };
        }
      }
    }

    return el;
  });

  excalidrawAPI.updateScene({
    elements: updatedElements,
  });

  // Scroll to show the arranged frames
  setTimeout(() => {
    excalidrawAPI.scrollToContent(frames, {
      fitToContent: true,
      animate: true,
      duration: 500,
    });
  }, 100);
};
