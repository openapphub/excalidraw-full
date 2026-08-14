import { createPortal } from "react-dom";

import type { ExcalidrawImperativeAPI } from "@excalidraw/excalidraw/types";

import { PresentationControls } from "./PresentationControls";
import { usePresentationMode } from "./usePresentationMode";

interface PresentationModeProps {
  excalidrawAPI: ExcalidrawImperativeAPI | null;
}

export const PresentationMode: React.FC<PresentationModeProps> = ({
  excalidrawAPI,
}) => {
  const {
    isPresentationMode,
    currentSlide,
    totalSlides,
    nextSlide,
    prevSlide,
    toggleTheme,
    toggleFullscreen,
    endPresentation,
  } = usePresentationMode({ excalidrawAPI });

  if (!isPresentationMode || totalSlides === 0) {
    return null;
  }

  return createPortal(
    <PresentationControls
      currentSlide={currentSlide}
      totalSlides={totalSlides}
      onPrevSlide={prevSlide}
      onNextSlide={nextSlide}
      onToggleTheme={toggleTheme}
      onToggleFullscreen={toggleFullscreen}
      onEndPresentation={endPresentation}
    />,
    document.body,
  );
};
