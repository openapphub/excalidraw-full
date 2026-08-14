import { useCallback, useEffect, useRef } from "react";

import { useUIAppState } from "@excalidraw/excalidraw";

import type { ExcalidrawFrameLikeElement } from "@excalidraw/element/types";
import type { ExcalidrawImperativeAPI } from "@excalidraw/excalidraw/types";

import { atom, useAtom, useSetAtom } from "../../app-jotai";
import { closeWorkspaceSidebarAtom } from "../Settings/settingsState";

// Keep these atoms for PresentationPanel slide ordering (AstraDraw-specific feature)
// The core presentation state is now in AppState.presentationMode
export const slidesAtom = atom<ExcalidrawFrameLikeElement[]>([]);

// Legacy atoms - kept for backward compatibility during transition
// These sync with AppState.presentationMode
export const presentationModeAtom = atom(false);
export const currentSlideAtom = atom(0);

// Atom for custom slide order (frame IDs in presentation order)
export const slideOrderAtom = atom<string[]>([]);

export interface UsePresentationModeOptions {
  excalidrawAPI: ExcalidrawImperativeAPI | null;
}

// Animation duration for slide transitions (ms)
const SLIDE_TRANSITION_DURATION = 800;

// How much of the viewport the frame should fill
const PRESENTATION_VIEWPORT_ZOOM_FACTOR = 1.0;

export const usePresentationMode = ({
  excalidrawAPI,
}: UsePresentationModeOptions) => {
  // Get presentation state from AppState (source of truth)
  const appState = useUIAppState();
  const presentationMode = appState.presentationMode;

  // Derived state from AppState
  const isPresentationMode = presentationMode?.active ?? false;
  const currentSlide = presentationMode?.currentSlide ?? 0;
  const slideIds = presentationMode?.slides ?? [];

  // Local state for slide elements (needed for PresentationPanel)
  const [slides, setSlides] = useAtom(slidesAtom);

  // AstraDraw-specific: workspace sidebar control
  const closeWorkspaceSidebar = useSetAtom(closeWorkspaceSidebarAtom);

  // Fullscreen state (browser API, not in AppState)
  const isFullscreenRef = useRef(false);

  // Get frames sorted alphabetically by name
  const getFrames = useCallback((): ExcalidrawFrameLikeElement[] => {
    if (!excalidrawAPI) {
      return [];
    }

    const elements = excalidrawAPI.getSceneElements();
    const frames: ExcalidrawFrameLikeElement[] = [];

    for (const el of elements) {
      if (el.type === "frame" && !el.isDeleted) {
        frames.push(el as ExcalidrawFrameLikeElement);
      }
    }

    // Sort frames alphabetically by name, then by creation order
    return frames.sort((a, b) => {
      const nameA = a.name || `Frame ${a.id}`;
      const nameB = b.name || `Frame ${b.id}`;
      return nameA.localeCompare(nameB, undefined, { numeric: true });
    });
  }, [excalidrawAPI]);

  // Navigate to a specific slide
  const goToSlide = useCallback(
    (slideIndex: number) => {
      if (!excalidrawAPI || !presentationMode?.active) {
        return;
      }

      const currentSlides = presentationMode.slides;
      if (currentSlides.length === 0) {
        return;
      }

      const targetIndex = Math.max(
        0,
        Math.min(slideIndex, currentSlides.length - 1),
      );
      const frameId = currentSlides[targetIndex];
      const elements = excalidrawAPI.getSceneElements();
      const frame = elements.find((el) => el.id === frameId);

      if (frame) {
        excalidrawAPI.scrollToContent(frame, {
          fitToViewport: true,
          viewportZoomFactor: PRESENTATION_VIEWPORT_ZOOM_FACTOR,
          animate: true,
          duration: SLIDE_TRANSITION_DURATION,
        });
      }

      excalidrawAPI.updateScene({
        appState: {
          presentationMode: {
            ...presentationMode,
            currentSlide: targetIndex,
          },
        },
      });
    },
    [excalidrawAPI, presentationMode],
  );

  // Navigation functions
  const nextSlide = useCallback(() => {
    if (!presentationMode?.active) {
      return;
    }
    const { currentSlide: current, slides: currentSlides } = presentationMode;
    if (current < currentSlides.length - 1) {
      goToSlide(current + 1);
    }
  }, [presentationMode, goToSlide]);

  const prevSlide = useCallback(() => {
    if (!presentationMode?.active) {
      return;
    }
    const { currentSlide: current } = presentationMode;
    if (current > 0) {
      goToSlide(current - 1);
    }
  }, [presentationMode, goToSlide]);

  // Toggle theme during presentation
  const toggleTheme = useCallback(() => {
    if (!excalidrawAPI) {
      return;
    }

    const currentTheme = excalidrawAPI.getAppState().theme;
    const newTheme = currentTheme === "dark" ? "light" : "dark";

    excalidrawAPI.updateScene({
      appState: { theme: newTheme },
    });
  }, [excalidrawAPI]);

  // Toggle fullscreen (browser API - stays in app layer)
  const toggleFullscreen = useCallback(async () => {
    try {
      if (!document.fullscreenElement) {
        await document.documentElement.requestFullscreen();
        isFullscreenRef.current = true;
      } else {
        await document.exitFullscreen();
        isFullscreenRef.current = false;
      }
    } catch (err) {
      console.error("Fullscreen error:", err);
    }
  }, []);

  // Start presentation with AstraDraw-specific setup
  const startPresentation = useCallback(() => {
    if (!excalidrawAPI) {
      return;
    }

    // Use existing ordered slides if already set (from PresentationPanel),
    // otherwise fall back to alphabetically sorted frames
    const frames = slides.length > 0 ? slides : getFrames();
    if (frames.length === 0) {
      excalidrawAPI.setToast({
        message: "No frames found. Add frames to create slides.",
        duration: 3000,
        closable: true,
      });
      return;
    }

    // AstraDraw-specific: Close workspace sidebar before starting
    closeWorkspaceSidebar();

    // AstraDraw-specific: Close default sidebar (right)
    excalidrawAPI.toggleSidebar({ name: "default", force: false });

    // AstraDraw-specific: Add presentation mode class to body for CSS hiding
    document.body.classList.add("excalidraw-presentation-mode");

    // Save current state and enter presentation mode
    const currentAppState = excalidrawAPI.getAppState();
    const slideIds = frames.map((f) => f.id);

    excalidrawAPI.updateScene({
      appState: {
        presentationMode: {
          active: true,
          currentSlide: 0,
          slides: slideIds,
          originalTheme: currentAppState.theme,
          originalFrameRendering: { ...currentAppState.frameRendering },
        },
        viewModeEnabled: true,
        zenModeEnabled: true,
        frameRendering: {
          ...currentAppState.frameRendering,
          outline: false,
          name: false,
        },
      },
    });

    // Navigate to first slide (laser is now implicit - any pointer draws laser trail)
    setTimeout(() => {
      if (frames[0]) {
        excalidrawAPI.scrollToContent(frames[0], {
          fitToViewport: true,
          viewportZoomFactor: PRESENTATION_VIEWPORT_ZOOM_FACTOR,
          animate: true,
          duration: SLIDE_TRANSITION_DURATION,
        });
      }
    }, 50);
  }, [excalidrawAPI, slides, getFrames, closeWorkspaceSidebar]);

  // End presentation with AstraDraw-specific cleanup
  const endPresentation = useCallback(async () => {
    if (!excalidrawAPI || !presentationMode?.active) {
      return;
    }

    // Exit fullscreen if active (browser API)
    if (document.fullscreenElement) {
      try {
        await document.exitFullscreen();
      } catch (err) {
        console.error("Exit fullscreen error:", err);
      }
    }

    // AstraDraw-specific: Remove presentation mode class from body
    document.body.classList.remove("excalidraw-presentation-mode");

    // Clear local slides state
    setSlides([]);

    // Build restored appState - use type assertion since updateScene accepts partial updates
    const restoredAppState = {
      presentationMode: null,
      viewModeEnabled: false,
      zenModeEnabled: false,
      ...(presentationMode.originalTheme
        ? { theme: presentationMode.originalTheme }
        : {}),
      ...(presentationMode.originalFrameRendering
        ? { frameRendering: presentationMode.originalFrameRendering }
        : {}),
    } as Parameters<typeof excalidrawAPI.updateScene>[0]["appState"];

    excalidrawAPI.updateScene({
      appState: restoredAppState,
    });

    // Reset to selection tool
    excalidrawAPI.setActiveTool({ type: "selection" });
  }, [excalidrawAPI, presentationMode, setSlides]);

  // Handle fullscreen change events
  useEffect(() => {
    const handleFullscreenChange = () => {
      isFullscreenRef.current = !!document.fullscreenElement;
    };

    document.addEventListener("fullscreenchange", handleFullscreenChange);
    return () =>
      document.removeEventListener("fullscreenchange", handleFullscreenChange);
  }, []);

  // Sync CSS class with presentation mode state
  useEffect(() => {
    if (isPresentationMode) {
      document.body.classList.add("excalidraw-presentation-mode");
    } else {
      document.body.classList.remove("excalidraw-presentation-mode");
    }
  }, [isPresentationMode]);

  // Keyboard handling - action system handles most keys via actionPresentation.ts
  // But we need to handle F for fullscreen (browser API) here
  useEffect(() => {
    if (!isPresentationMode) {
      return;
    }

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === "f" || e.key === "F") {
        if (!e.ctrlKey && !e.metaKey && !e.altKey) {
          e.preventDefault();
          toggleFullscreen();
        }
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isPresentationMode, toggleFullscreen]);

  return {
    isPresentationMode,
    currentSlide,
    slides,
    setSlides,
    totalSlides: slideIds.length || slides.length,
    startPresentation,
    endPresentation,
    nextSlide,
    prevSlide,
    goToSlide,
    toggleTheme,
    toggleFullscreen,
    getFrames,
  };
};
