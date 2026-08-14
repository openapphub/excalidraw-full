import { useState, useCallback } from "react";

import { DefaultSidebar, Sidebar, useUIAppState } from "@excalidraw/excalidraw";
import {
  messageCircleIcon,
  presentationIcon,
  stickerIcon,
  videoIcon,
} from "@excalidraw/excalidraw/components/icons";

import type { ExcalidrawImperativeAPI } from "@excalidraw/excalidraw/types";

import { CommentsSidebar } from "../Comments";
import { PresentationPanel } from "../Presentation";
import { StickersPanel } from "../Stickers";
import { TalktrackPanel, TalktrackManager } from "../Talktrack";

export interface AppSidebarProps {
  excalidrawAPI: ExcalidrawImperativeAPI | null;
  sceneId: string | null;
}

export const AppSidebar: React.FC<AppSidebarProps> = ({
  excalidrawAPI,
  sceneId,
}) => {
  const { openSidebar } = useUIAppState();
  const [isRecordingDialogOpen, setIsRecordingDialogOpen] = useState(false);
  const [recordingsRefreshKey, setRecordingsRefreshKey] = useState(0);

  const handleStartRecording = useCallback(() => {
    setIsRecordingDialogOpen(true);
  }, []);

  const handleCloseRecordingDialog = useCallback(() => {
    setIsRecordingDialogOpen(false);
  }, []);

  // Called when a recording is saved to refresh the panel
  const handleRecordingSaved = useCallback(() => {
    setRecordingsRefreshKey((prev) => prev + 1);
  }, []);

  return (
    <>
      <DefaultSidebar>
        <DefaultSidebar.TabTriggers>
          <Sidebar.TabTrigger
            tab="comments"
            style={{ opacity: openSidebar?.tab === "comments" ? 1 : 0.4 }}
          >
            {messageCircleIcon}
          </Sidebar.TabTrigger>
          <Sidebar.TabTrigger
            tab="stickers"
            style={{ opacity: openSidebar?.tab === "stickers" ? 1 : 0.4 }}
          >
            {stickerIcon}
          </Sidebar.TabTrigger>
          <Sidebar.TabTrigger
            tab="talktrack"
            style={{ opacity: openSidebar?.tab === "talktrack" ? 1 : 0.4 }}
          >
            {videoIcon}
          </Sidebar.TabTrigger>
          <Sidebar.TabTrigger
            tab="presentation"
            style={{ opacity: openSidebar?.tab === "presentation" ? 1 : 0.4 }}
          >
            {presentationIcon}
          </Sidebar.TabTrigger>
        </DefaultSidebar.TabTriggers>
        <Sidebar.Tab tab="comments">
          <CommentsSidebar
            sceneId={sceneId ?? undefined}
            excalidrawAPI={excalidrawAPI}
          />
        </Sidebar.Tab>
        <Sidebar.Tab tab="presentation">
          <PresentationPanel excalidrawAPI={excalidrawAPI} />
        </Sidebar.Tab>
        <Sidebar.Tab tab="stickers">
          <StickersPanel excalidrawAPI={excalidrawAPI} />
        </Sidebar.Tab>
        <Sidebar.Tab tab="talktrack">
          <TalktrackPanel
            key={recordingsRefreshKey}
            excalidrawAPI={excalidrawAPI}
            onStartRecording={handleStartRecording}
            sceneId={sceneId}
          />
        </Sidebar.Tab>
      </DefaultSidebar>

      {/* Talktrack recording manager (handles dialogs and recording toolbar) */}
      <TalktrackManager
        excalidrawAPI={excalidrawAPI}
        isRecordingDialogOpen={isRecordingDialogOpen}
        onCloseRecordingDialog={handleCloseRecordingDialog}
        sceneId={sceneId}
        onRecordingSaved={handleRecordingSaved}
      />
    </>
  );
};
