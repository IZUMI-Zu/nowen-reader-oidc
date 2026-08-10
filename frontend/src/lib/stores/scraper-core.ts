/**
 * 刮削状态管理 — 核心状态与订阅机制
 *
 * 包含模块级单例 state、初始值、subscribe/notify 机制。
 * 所有 action 文件通过 getState() 和 notify() 来读写状态。
 */

import type { ScraperState } from "./scraper-types";
import { BATCH_SCRAPE_FIELDS } from "./scraper-types";

/* ── 模块级状态 ── */
let state: ScraperState = {
  stats: null,
  statsLoading: false,
  batchRunning: false,
  batchMode: "standard",
  scrapeScope: "missing",
  updateTitle: false,
  skipCover: false,
  currentProgress: null,
  batchDone: null,
  completedItems: [],
  showResults: true,
  // 书库管理
  activeTab: "scrape",
  libraryItems: [],
  libraryLoading: false,
  librarySearch: "",
  libraryMetaFilter: "all",
  libraryContentType: "comic",
  librarySortBy: (typeof globalThis !== "undefined" && globalThis.localStorage?.getItem("scraper-sortBy") as ScraperState["librarySortBy"]) || "title",
  librarySortOrder: (typeof globalThis !== "undefined" && globalThis.localStorage?.getItem("scraper-sortOrder") as "asc" | "desc") || "asc",
  libraryPage: 1,
  libraryPageSize: 20,
  libraryTotalPages: 1,
  libraryTotal: 0,
  selectedIds: new Set(),
  // 详情面板
  focusedItemId: null,
  // 批量编辑模式
  batchEditMode: false,
  batchEditNames: new Map(),
  batchEditSaving: false,
  batchEditResults: null,
  aiRenameLoading: false,
  // AI 聊天面板
  aiChatOpen: false,
  aiChatMessages: [],
  aiChatLoading: false,
  aiChatInput: "",
  // 引导教程系统
  guideActive: false,
  guideCurrentStep: 0,
  guideDismissed: typeof globalThis !== "undefined" ? globalThis.localStorage?.getItem("scraper-guide-dismissed") === "true" : false,
  helpPanelOpen: false,
  helpSearchQuery: "",
  // 合集管理
  collectionPanelOpen: false,
  collectionGroups: [],
  collectionGroupsLoading: false,
  collectionDetail: null,
  collectionDetailLoading: false,
  collectionAutoSuggestions: [],
  collectionAutoLoading: false,
  collectionCreateDialog: false,
  collectionAddToGroupDialog: false,
  collectionEditingId: null,
  collectionEditingName: "",
  // 文件夹模式
  viewMode: "list",
  folderTree: null,
  folderTreeLoading: false,
  selectedFolderPath: null,
  folderSearch: "",
  folderScrapeRunning: false,
  folderScrapeProgress: null,
  folderScrapeDone: null,
  // 系列模式
  scraperGroups: [],
  scraperGroupsLoading: false,
  scraperGroupFocusedId: null,
  scraperGroupSelectedIds: new Set<number>(),
  scraperGroupMetaFilter: "all",
  scraperGroupSortBy: "name",
  scraperGroupSortAsc: true,
  scraperGroupSearch: "",
  scraperGroupContentType: "",
  // 系列分页
  groupPage: 1,
  groupPageSize: 20,
  // 系列批量刮削
  groupBatchRunning: false,
  groupBatchProgress: null,
  groupBatchDone: null,
  // 批量在线刮削
  groupBatchScrapeDialogOpen: false,
  groupBatchScrapeMode: "online" as const,
  groupBatchScrapeFields: new Set([
    "author", "description", "genre", "publisher", "language", "year", "cover", "tags", "rating",
  ]),
  groupBatchScrapeOverwrite: true,
  groupBatchScrapeSyncTags: true,
  groupBatchScrapeSyncToVolumes: true,
  groupBatchScrapeSources: ["anilist", "bangumi", "mangadex", "mangaupdates", "kitsu"],
  groupBatchScrapePreview: null,
  groupBatchScrapePreviewLoading: false,
  groupBatchScrapeApplying: false,
  groupBatchScrapeResult: null,
  // 脏数据检测与清理
  dirtyIssues: [],
  dirtyStats: null,
  dirtyDetecting: false,
  dirtyCleaning: false,
  cleanupResult: null,
};

const listeners = new Set<() => void>();

/* ── 订阅机制 ── */
export function notify() {
  // 创建新的 state 引用以触发 useSyncExternalStore 重渲染
  state = { ...state };
  listeners.forEach((fn) => fn());
}

export function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function getSnapshot(): ScraperState {
  return state;
}

/**
 * 获取当前 state 的可变引用（仅供 action 文件内部使用）
 */
export function getState(): ScraperState {
  return state;
}

// 导出 BATCH_SCRAPE_FIELDS 以便其他模块使用
export { BATCH_SCRAPE_FIELDS };
