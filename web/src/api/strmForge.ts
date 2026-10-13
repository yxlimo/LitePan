import { http } from "./client";

export interface StrmForgeTask {
  id: string;
  task_name: string;
  account_id: number;
  source_directory: string;
  source_directory_id: string;
  /** 扫描目录往下递归的层数，后端默认 5、最大 10。 */
  max_depth: number;
  status: string;
  created_at?: string;
  updated_at?: string;
  last_scan_at?: string;
  last_scan_result?: StrmForgeScanResult;
}

export interface StrmForgeScanFailure {
  dir_path: string;
  message: string;
}

export interface StrmForgeScanResult {
  mode: string;
  added: number;
  resurrected: number;
  changed: number;
  removed: number;
  pending: number;
  incomplete?: boolean;
  failures?: StrmForgeScanFailure[];
  error?: string;
}

export interface StrmForgeTaskInput {
  task_name: string;
  account_id: number;
  source_directory: string;
  source_directory_id: string;
  /** 0 表示由后端补默认值。 */
  max_depth?: number;
}

export interface StrmForgeScanProgress {
  visited_dirs: number;
  files: number;
  current_path: string;
}

export interface StrmForgeScanJob {
  task_id: string;
  state: "running" | "done" | "failed";
  mode: "full" | "diff" | "";
  started_at: string;
  finished_at?: string;
  visited_dirs: number;
  total_files: number;
  added: number;
  resurrected: number;
  changed: number;
  removed: number;
  pending_deletions: number;
  incomplete?: boolean;
  failures?: StrmForgeScanFailure[];
  error?: string;
  progress?: StrmForgeScanProgress;
}

/** 扫描结果里的一个文件节点。 */
export interface StrmForgeNode {
  rel_path: string;
  parent_path: string;
  name: string;
  size: number;
  modified_time?: string;
  last_seen_at?: string;
  /** 仅待确认删除列表里有值。 */
  deleted_at?: string;
}

export interface StrmForgeNodePage {
  nodes: StrmForgeNode[];
  total: number;
  /** 还有下一页时给出下一页的偏移量。 */
  next_offset: number | null;
}

export function fetchStrmForgeTasks() {
  return http.get<StrmForgeTask[]>("/strmforge/tasks");
}

export function createStrmForgeTask(input: StrmForgeTaskInput) {
  return http.post<StrmForgeTask>("/strmforge/tasks", input);
}

export function deleteStrmForgeTask(id: string) {
  return http.del<{ id: string }>(`/strmforge/tasks/${id}`);
}

/** 触发后台扫描，立即返回；进度轮询 fetchStrmForgeScan。 */
export function startStrmForgeScan(id: string) {
  return http.post<StrmForgeScanJob>(`/strmforge/tasks/${id}/scan`);
}

export function fetchStrmForgeScan(id: string) {
  return http.get<StrmForgeScanJob | null>(`/strmforge/tasks/${id}/scan`);
}

/** 扫描结果分页列表，一个文件一行。 */
export function fetchStrmForgeNodes(
  id: string,
  opts: { q?: string; limit?: number; offset?: number } = {},
) {
  return http.get<StrmForgeNodePage>(`/strmforge/tasks/${id}/nodes`, {
    q: opts.q || undefined,
    limit: opts.limit,
    offset: opts.offset,
  });
}

/** 待确认删除列表：本次扫描没读到的文件。 */
export function fetchStrmForgeDeletions(id: string) {
  return http.get<StrmForgeNode[]>(`/strmforge/tasks/${id}/deletions`);
}

/**
 * 确认删除：物理移除选中的墓碑行。
 * rel_paths 与 all 必须至少给一个；传 rel_paths: [] 且 all 不为 true 会被后端拒绝。
 */
export function confirmStrmForgeDeletions(id: string, relPaths: string[], all = false) {
  return http.post<{ removed: number }>(`/strmforge/tasks/${id}/deletions/confirm`, {
    rel_paths: relPaths,
    all,
  });
}

/** 误报：把选中的墓碑恢复成活跃。all 的语义与确认删除一致。 */
export function restoreStrmForgeDeletions(id: string, relPaths: string[], all = false) {
  return http.post<{ restored: number }>(`/strmforge/tasks/${id}/deletions/restore`, {
    rel_paths: relPaths,
    all,
  });
}