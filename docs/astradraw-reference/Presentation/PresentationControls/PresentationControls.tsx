import clsx from "clsx";
import { useCallback, useEffect, useRef, useState } from "react";

import {
  MoonIcon,
  SunIcon,
  fullscreenIcon,
} from "@excalidraw/excalidraw/components/icons";
import { useUIAppState } from "@excalidraw/excalidraw/context/ui-appState";
import { t } from "@excalidraw/excalidraw/i18n";

import styles from "./PresentationControls.module.scss";

interface PresentationControlsProps {
  currentSlide: number;
  totalSlides: number;
  onPrevSlide: () => void;
  onNextSlide: () => void;
  onToggleTheme: () => void;
  onToggleFullscreen: () => void;
  onEndPresentation: () => void;
}

const FADE_DELAY = 3000;

export const PresentationControls: React.FC<PresentationControlsProps> = ({
  currentSlide,
  totalSlides,
  onPrevSlide,
  onNextSlide,
  onToggleTheme,
  onToggleFullscreen,
  onEndPresentation,
}) => {
  const { theme } = useUIAppState();
  const [isVisible, setIsVisible] = useState(true);
  const fadeTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const resetFadeTimer = useCallback(() => {
    setIsVisible(true);

    if (fadeTimeoutRef.current) {
      clearTimeout(fadeTimeoutRef.current);
    }

    fadeTimeoutRef.current = setTimeout(() => {
      setIsVisible(false);
    }, FADE_DELAY);
  }, []);

  // Reset timer on user interaction
  useEffect(() => {
    const handleMouseMove = () => resetFadeTimer();
    const handleMouseDown = () => resetFadeTimer();

    window.addEventListener("mousemove", handleMouseMove);
    window.addEventListener("mousedown", handleMouseDown);

    // Initial timer
    resetFadeTimer();

    return () => {
      window.removeEventListener("mousemove", handleMouseMove);
      window.removeEventListener("mousedown", handleMouseDown);

      if (fadeTimeoutRef.current) {
        clearTimeout(fadeTimeoutRef.current);
      }
    };
  }, [resetFadeTimer]);

  const handleControlsMouseEnter = () => {
    setIsVisible(true);
    if (fadeTimeoutRef.current) {
      clearTimeout(fadeTimeoutRef.current);
    }
  };

  const handleControlsMouseLeave = () => {
    resetFadeTimer();
  };

  return (
    <div
      className={clsx(styles.controls, {
        [styles.hidden]: !isVisible,
      })}
      onMouseEnter={handleControlsMouseEnter}
      onMouseLeave={handleControlsMouseLeave}
    >
      <div className={styles.bar}>
        {/* Navigation */}
        <button
          className={styles.button}
          onClick={onPrevSlide}
          disabled={currentSlide === 0}
          title={`${t("presentation.previousSlide")} (←)`}
          aria-label={t("presentation.previousSlide")}
        >
          <svg
            width="20"
            height="20"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
          >
            <path d="M15 18l-6-6 6-6" />
          </svg>
        </button>

        <span className={styles.counter}>
          {t("presentation.slide")} {currentSlide + 1}/{totalSlides}
        </span>

        <button
          className={styles.button}
          onClick={onNextSlide}
          disabled={currentSlide === totalSlides - 1}
          title={`${t("presentation.nextSlide")} (→)`}
          aria-label={t("presentation.nextSlide")}
        >
          <svg
            width="20"
            height="20"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
          >
            <path d="M9 18l6-6-6-6" />
          </svg>
        </button>

        <div className={styles.divider} />

        {/* Theme toggle */}
        <button
          className={styles.button}
          onClick={onToggleTheme}
          title={t("presentation.toggleTheme")}
          aria-label={t("presentation.toggleTheme")}
        >
          {theme === "dark" ? SunIcon : MoonIcon}
        </button>

        {/* Fullscreen */}
        <button
          className={styles.button}
          onClick={onToggleFullscreen}
          title={`${t("presentation.toggleFullscreen")} (F)`}
          aria-label={t("presentation.toggleFullscreen")}
        >
          {fullscreenIcon}
        </button>

        <div className={styles.divider} />

        {/* End presentation */}
        <button
          className={clsx(styles.button, styles.buttonEnd)}
          onClick={onEndPresentation}
          title={`${t("presentation.endPresentation")} (Esc)`}
        >
          {t("presentation.endPresentation")}
        </button>
      </div>
    </div>
  );
};
