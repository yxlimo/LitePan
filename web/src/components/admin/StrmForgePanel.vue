<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { storeToRefs } from "pinia";
import { getApiErrorMessage } from "@/api/client";
import {
  createStrmForgeTask,
  deleteStrmForgeTask,
  fetchStrmForgeScan,
  fetchStrmForgeTasks,
  startStrmForgeScan,
  type StrmForgeScanJob,
  type StrmForgeScanResult,
  type StrmForgeTask,
} from "@/api/strmForge";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import StrmForgeDeletionsPanel from "@/components/admin/StrmForgeDeletionsPanel.vue";
import StrmForgeNodesPanel from "@/components/admin/StrmForgeNodesPanel.vue";
import AccountFolderField from "@/components/admin/AccountFolderField.vue";
import AdminTableActionBtn from "@/components/admin/AdminTableActionBtn.vue";
import AdminRowActions from "@/components/admin/AdminRowActions.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppModal from "@/components/base/AppModal.vue";
import AppTabBar from "@/components/base/AppTabBar.vue";
import FormField from "@/components/base/FormField.vue";
import FolderPickerModal from "@/components/file/FolderPickerModal.vue";
import { useAccountPathLabel } from "@/composables/useAccountPathLabel";
import { confirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import { useAccountsStore } from "@/stores/accounts";
import { formatTime } from "@/utils/format";
import "@/styles/admin-shared.css";
import "@/styles/admin-table.css";

const MAX_TASK_NAME = 10;

const accountsStore = useAccountsStore();
const { accounts } = storeToRefs(accountsStore);
const activeAccounts = computed(() => accounts.value.filter((a) => a.is_active));

const tasks = ref<StrmForgeTask[]>([]);
const loading = ref(false);
const listReady = ref(false);
const submitting = ref(false);
const dialogOpen = ref(false);
const pickerOpen = ref(false);

// 扫描状态：key 为 taskID，后台任务用轮询跟踪。
const scanJobs = ref<Record<string, StrmForgeScanJob | null>>({});
let pollTimer: ReturnType<typeof setTimeout> | null = null;

// 页签：任务列表 / 扫描结果 / 待确认删除。
const VIEW_TASKS = "tasks";
const VIEW_NODES = "nodes";
const VIEW_DELETIONS = "deletions";
const view = ref<string>(VIEW_TASKS);
// 传给子页面的任务；为空时子页面自己选第一个。
const activeTaskID = ref<string | null>(null);

const viewTabs = [
  { key: VIEW_TASKS, label: "任务" },
  { key: VIEW_NODES, label: "扫描结果" },
  { key: VIEW_DELETIONS, label: "待确认删除" },
];

function openView(next: string, task?: StrmForgeTask) {
  if (task) activeTaskID.value = task.id;
  view.value = next;
}

function jobOf(taskID: string): StrmForgeScanJob | null {
  return scanJobs.value[taskID] ?? null;
}

function isScanning(taskID: string) {
  return jobOf(taskID)?.state === "running";
}

// 服务重启后内存里的 job 会丢，回落到任务行持久化的扫描结果结构。
function summaryOf(task: StrmForgeTask): StrmForgeScanJob | StrmForgeScanResult | null {
  return jobOf(task.id) ?? task.last_scan_result ?? null;
}

function scanSummary(task: StrmForgeTask) {
  const job = jobOf(task.id);
  if (job?.state === "running") return "扫描中…";
  const result = job ?? task.last_scan_result ?? null;
  if (!result) return "尚未扫描";
  if (result.error) return `失败：${result.error}`;
  const mode = result.mode === "full" ? "全量" : "增量";
  return `${mode}：新增 ${result.added}、变更 ${result.changed}、待确认删除 ${result.removed}`;
}

const MAX_DEPTH_LIMIT = 10;
const DEFAULT_MAX_DEPTH = 5;

function emptyForm() {
  return {
    task_name: "",
    account_id: 0,
    source_directory: "",
    source_directory_id: "",
    max_depth: DEFAULT_MAX_DEPTH,
  };
}

const form = reactive(emptyForm());

const { display: sourceDirDisplay, title: sourceDirTitle } = useAccountPathLabel({
  accountId: computed(() => form.account_id),
  path: computed(() => form.source_directory),
  accounts,
});

function accountName(accountId: number) {
  return accounts.value.find((a) => a.id === accountId)?.name ?? `#${accountId}`;
}

async function loadTasks() {
  loading.value = true;
  try {
    tasks.value = await fetchStrmForgeTasks();
    // 补拉各任务的最近扫描状态，刷新页面后也能看到结果。
    await Promise.all(
      tasks.value.map(async (task) => {
        try {
          scanJobs.value[task.id] = await fetchStrmForgeScan(task.id);
        } catch {
          scanJobs.value[task.id] = null;
        }
      }),
    );
    if (tasks.value.some((t) => isScanning(t.id))) schedulePoll();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载 STRM 映射任务失败"));
  } finally {
    loading.value = false;
    listReady.value = true;
  }
}

function openCreate() {
  Object.assign(form, emptyForm());
  dialogOpen.value = true;
}

function onFolderPicked(payload: { accountId: number; parentId: string; path: string }) {
  form.account_id = payload.accountId;
  form.source_directory = payload.path || "/";
  form.source_directory_id = payload.parentId;
  pickerOpen.value = false;
}

async function submitTask() {
  if (!form.task_name.trim()) {
    toast.error("请输入任务名称");
    return;
  }
  if (!form.account_id || (!form.source_directory.trim() && !form.source_directory_id.trim())) {
    toast.error("请选择扫描目录");
    return;
  }
  if (form.max_depth < 1 || form.max_depth > MAX_DEPTH_LIMIT) {
    toast.error(`扫描深度需在 1–${MAX_DEPTH_LIMIT} 层之间`);
    return;
  }
  submitting.value = true;
  try {
    await createStrmForgeTask({
      task_name: form.task_name.trim(),
      account_id: form.account_id,
      source_directory: form.source_directory,
      source_directory_id: form.source_directory_id,
      max_depth: form.max_depth,
    });
    toast.success("任务已创建");
    dialogOpen.value = false;
    await loadTasks();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "保存任务失败"));
  } finally {
    submitting.value = false;
  }
}

async function handleDelete(task: StrmForgeTask) {
  try {
    await confirm({
      title: "删除 STRM 映射任务",
      message: `确定删除任务「${task.task_name}」吗？`,
      icon: "trash",
      confirmText: "删除",
      danger: true,
    });
  } catch {
    return;
  }
  try {
    await deleteStrmForgeTask(task.id);
    toast.success("任务已删除");
    await loadTasks();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "删除任务失败"));
  }
}

async function handleScan(task: StrmForgeTask) {
  if (isScanning(task.id)) return;
  try {
    scanJobs.value[task.id] = await startStrmForgeScan(task.id);
    toast.success("扫描已开始，正在后台运行");
    schedulePoll();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "启动扫描失败"));
  }
}

// 后台扫描用轮询跟踪；没有任务在跑就停下来。
async function schedulePoll() {
  if (pollTimer !== null) return;
  pollTimer = setTimeout(async () => {
    pollTimer = null;
    let running = false;
    for (const task of tasks.value) {
      if (!isScanning(task.id)) continue;
      running = true;
      try {
        const job = await fetchStrmForgeScan(task.id);
        scanJobs.value[task.id] = job;
      } catch (e) {
        toast.error(getApiErrorMessage(e, "查询扫描进度失败"));
      }
    }
    if (running) await loadTasks();
    if (running || tasks.value.some((t) => isScanning(t.id))) schedulePoll();
  }, 1500);
}

defineExpose({ openCreate, loadTasks, refreshing: loading });

onBeforeUnmount(() => {
  if (pollTimer !== null) clearTimeout(pollTimer);
});

onMounted(loadTasks);
</script>

<template>
  <div class="strmforge-panel">
    <AppTabBar v-model="view" :tabs="viewTabs" class="strmforge-viewtabs" />

    <StrmForgeNodesPanel v-if="view === VIEW_NODES" :task-id="activeTaskID" />
    <StrmForgeDeletionsPanel v-else-if="view === VIEW_DELETIONS" :task-id="activeTaskID" />

    <template v-else>
    <AdminEmptyState
      v-if="listReady && !loading && !tasks.length"
      icon="hand-folder"
      title="还没有 STRM 映射任务"
      description="添加任务后，将按任务配置扫描目录并生成 STRM 文件。"
    >
      <AppButton type="button" variant="primary" @click="openCreate">添加第一个任务</AppButton>
    </AdminEmptyState>

    <div v-else-if="tasks.length" class="admin-panel-table-wrap">
      <table class="admin-table">
        <thead>
          <tr>
            <th>任务</th>
            <th>扫描目录</th>
            <th>上次扫描</th>
            <th class="admin-table__actions">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="task in tasks" :key="task.id">
            <td>
              <div class="strmforge-task-name">{{ task.task_name }}</div>
              <div class="admin-account-sub">{{ accountName(task.account_id) }}</div>
            </td>
            <td class="strmforge-path" :title="task.source_directory">
              {{ task.source_directory || "—" }}
            </td>
            <td>
              <div class="strmforge-scan-state">{{ scanSummary(task) }}</div>
              <div v-if="task.last_scan_at" class="admin-account-sub">
                {{ formatTime(task.last_scan_at) }}
              </div>
              <div v-if="summaryOf(task)?.incomplete" class="strmforge-scan-warning">
                本次读到的内容不足一半，已跳过删除判定
              </div>
              <div v-if="summaryOf(task)?.failures?.length" class="strmforge-scan-warning">
                {{ summaryOf(task)?.failures?.length }} 个目录读取失败
              </div>
            </td>
            <td class="admin-table__actions">
              <AdminRowActions>
                <AdminTableActionBtn
                  icon="rotate"
                  :title="isScanning(task.id) ? '扫描进行中' : '扫描目录'"
                  :disabled="isScanning(task.id)"
                  @click="handleScan(task)"
                />
                <AdminTableActionBtn
                  icon="log"
                  title="扫描结果"
                  @click="openView(VIEW_NODES, task)"
                />
                <AdminTableActionBtn
                  icon="edit"
                  title="待确认删除"
                  @click="openView(VIEW_DELETIONS, task)"
                />
                <AdminTableActionBtn icon="delete" title="删除任务" danger @click="handleDelete(task)" />
              </AdminRowActions>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    </template>

    <AppModal :open="dialogOpen" title="新建 STRM 映射任务" size="md" @close="dialogOpen = false">
      <div class="modal-form">
        <div class="modal-form__row">
          <FormField label="任务名称">
            <AppInput v-model="form.task_name" :maxlength="MAX_TASK_NAME" placeholder="例如：电影映射" />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="扫描目录">
            <AccountFolderField
              :display="sourceDirDisplay"
              :title="sourceDirTitle"
              placeholder="点击浏览选择账号及目录"
              @browse="pickerOpen = true"
            />
          </FormField>
        </div>
        <div class="modal-form__row">
          <FormField label="扫描深度" hint="从扫描目录往下递归的层数，1–10">
            <AppInput v-model.number="form.max_depth" type="number" :min="1" :max="MAX_DEPTH_LIMIT" />
          </FormField>
        </div>
      </div>
      <template #footer>
        <AppButton type="button" variant="ghost" @click="dialogOpen = false">取消</AppButton>
        <AppButton type="button" variant="primary" :disabled="submitting" @click="submitTask">
          {{ submitting ? "保存中…" : "保存" }}
        </AppButton>
      </template>
    </AppModal>

    <FolderPickerModal
      :open="pickerOpen"
      selectable-account
      :accounts="activeAccounts"
      :account-id="form.account_id || null"
      :initial-path="form.source_directory"
      title="选择扫描目录"
      @close="pickerOpen = false"
      @resolve="onFolderPicked"
    />
  </div>
</template>

<style scoped>
.strmforge-panel {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.strmforge-task-name {
  font-weight: 600;
}

.strmforge-path {
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.strmforge-scan-state {
  font-size: 13px;
}

.strmforge-scan-warning {
  margin-top: 2px;
  font-size: 12px;
  color: var(--lp-warning, #b45309);
}

.strmforge-hint {
  margin: 0 0 12px;
  font-size: 13px;
  color: var(--lp-text-muted, #64748b);
  line-height: 1.6;
}

.strmforge-deletion-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
  max-height: 320px;
  overflow-y: auto;
}

.strmforge-deletion-item {
  padding: 8px 10px;
  border: 1px solid var(--lp-border, #e2e8f0);
  border-radius: 8px;
}

.strmforge-deletion-path {
  font-size: 13px;
  word-break: break-all;
}
</style>
