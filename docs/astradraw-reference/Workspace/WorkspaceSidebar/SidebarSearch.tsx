import React, { useRef, useEffect } from "react";
import { t } from "@excalidraw/excalidraw/i18n";

import { searchIcon } from "./icons";
import styles from "./WorkspaceSidebar.module.scss";

interface SidebarSearchProps {
  value: string;
  onChange: (value: string) => void;
  isOpen: boolean;
}

export const SidebarSearch: React.FC<SidebarSearchProps> = ({
  value,
  onChange,
  isOpen,
}) => {
  const searchInputRef = useRef<HTMLInputElement>(null);

  // Keyboard shortcut for search (Cmd/Ctrl + P)
  useEffect(() => {
    if (!isOpen) {
      return;
    }

    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "p") {
        e.preventDefault();
        searchInputRef.current?.focus();
      }
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [isOpen]);

  return (
    <div className={styles.search}>
      <span className={styles.searchIcon}>{searchIcon}</span>
      <input
        ref={searchInputRef}
        type="text"
        className={styles.searchInput}
        placeholder={t("workspace.quickSearch")}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => e.stopPropagation()}
        onKeyUp={(e) => e.stopPropagation()}
      />
      <span className={styles.searchHint}>⌘ + p</span>
    </div>
  );
};

export default SidebarSearch;
