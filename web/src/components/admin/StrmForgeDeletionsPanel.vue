<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  confirmStrmForgeDeletions,
  fetchStrmForgeDeletions,
  fetchStrmForgeTasks,
  restoreStrmForgeDeletions,
  type StrmForgeNode,
  type StrmForgeTask,
} from "@/api/strmForge";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import { confirm } from "@/composables/useConfirm";
import { toast } from "@/composables/useToast";
import { formatTime } from "@/utils/format";
import "@/styles/admin-shared.css";
import "@/styles/admin-table.css";

// 父级可指定初始任务（例如从任务列表点进来时）。
const props = defineProps<{ taskId?: string | null }>();

const tasks = ref<StrmForgeTask[]>([]);
const taskID = ref<string | null>(props.taskId ?? null);
const pending = ref<StrmForgeNode[]>([]);
const selected = ref<Set<string>>(new Set());
const loading = ref(false);
const busy = ref(false);
const listReady = ref(false);

const taskOptions = computed(() =>
  tasks.value.map((task) => ({ value: task.id, label: task.task_name })),
);

const currentTask = computed(() => tasks.value.find((task) => task.id === taskID.value) ?? null);
const allSelected = computed(
  () => pending.value.length > 0 && selected.value.size === pending.value.length,
);

async function loadTasks() {
  try {
    tasks.value = await fetchStrmForgeTasks();
    if (!taskID.value && tasks.value.length) {
      taskID.value = tasks.value[0].id;
      return;
    }
    if (taskID.value && !tasks.value.some((task) => task.id === taskID.value)) {
      taskID.value = tasks.value[0]?.id ?? null;
    }
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载任务失败"));
  }
}

async function loadPending() {
  if (!taskID.value) {
    pending.value = [];
    return;
  }
  loading.value = true;
  try {
    pending.value = await fetchStrmForgeDeletions(taskID.value);
    selected.value = new Set();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载待确认删除失败"));
  } finally {
    loading.value = false;
    listReady.value = true;
  }
}

function toggle(relPath: string) {
  const next = new Set(selected.value);
  if (next.has(relPath)) next.delete(relPath);
  else next.add(relPath);
  selected.value = next;
}

function toggleAll() {
  selected.value = allSelected.value ? new Set() : new Set(pending.value.map((n) => n.rel_path));
}

// 必须先勾选：没选中任何行时两个按钮都是禁用的，不会误操作。
function targetPaths(): string[] {
  return [...selected.value];
}

const nothingSelected = computed(() => selected.value.size === 0);

async function handleConfirm() {
  const task = currentTask.value;
  if (!task || nothingSelected.value) return;
  const paths = targetPaths();
  try {
    await confirm({
      title: "确认删除",
      message: `将把选中的 ${paths.length} 项从快照中物理删除，后续扫描不会再视为存在。确定吗？`,
      icon: "trash",
      confirmText: "确认删除",
      danger: true,
    });
  } catch {
    return;
  }
  busy.value = true;
  try {
    const result = await confirmStrmForgeDeletions(task.id, paths);
    toast.success(`已删除 ${result.removed} 条记录`);
    await loadPending();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "确认删除失败"));
  } finally {
    busy.value = false;
  }
}

async function handleRestore() {
  const task = currentTask.value;
  if (!task || nothingSelected.value) return;
  const paths = targetPaths();
  busy.value = true;
  try {
    const result = await restoreStrmForgeDeletions(task.id, paths);
    toast.success(`已恢复 ${result.restored} 项`);
    await loadPending();
  } catch (e) {
    toast.error(getApiErrorMessage(e, "恢复失败"));
  } finally {
    busy.value = false;
  }
}

function formatSize(bytes: number) {
  if (bytes <= 0) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let value = bytes;
  let idx = 0;
  while (value >= 1024 && idx < units.length - 1) {
    value /= 1024;
    idx += 1;
  }
  return `${value.toFixed(idx === 0 ? 0 : 1)} ${units[idx]}`;
}

watch(
  () => props.taskId,
  (next) => {
    if (next && next !== taskID.value) taskID.value = next;
  },
);

watch(taskID, () => {
  void loadPending();
});

onMounted(async () => {
  await loadTasks();
  if (taskID.value) await loadPending();
  else listReady.value = true;
});

defineExpose({ loadTasks, loadPending });
</script>

<template>
  <div class="strmforge-pending">
    <div class="strmforge-pending__bar">
      <AppSelect
        v-model="taskID"
        :options="taskOptions"
        :disabled="!tasks.length"
        placeholder="选择任务"
      />
      <div class="strmforge-pending__actions">
        <AppButton type="button" :disabled="busy || nothingSelected" @click="handleRestore">
          标记误报（恢复）
        </AppButton>
        <AppButton
          type="button"
          variant="danger"
          :disabled="busy || nothingSelected"
          @click="handleConfirm"
        >
          确认删除
        </AppButton>
      </div>
    </div>

    <p class="strmforge-pending__hint">
      这些文件在最近一次扫描中没有读到，可能真的被删除，也可能只是网盘临时没返回。
      勾选要处理的行，再确认删除（从快照中移除）或标记误报（恢复它们）。两个按钮在未勾选时不可用。
    </p>

    <AdminEmptyState
      v-if="listReady && !tasks.length"
      icon="hand-folder"
      title="还没有 STRM 映射任务"
      description="先在任务列表里添加任务并扫描目录。"
    />

    <AdminEmptyState
      v-else-if="listReady && !loading && !pending.length"
      icon="hand-folder"
      title="没有待确认的删除项"
      description="最近一次扫描没有发现读不到的文件。"
    />

    <div v-else class="admin-panel-table-wrap">
      <table class="admin-table">
        <thead>
          <tr>
            <th class="strmforge-pending__check">
              <input
                type="checkbox"
                :checked="allSelected"
                :indeterminate="selected.size > 0 && !allSelected"
                @change="toggleAll"
              />
            </th>
            <th>路径</th>
            <th>文件名</th>
            <th>大小</th>
            <th>修改时间</th>
            <th>标记时间</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in pending" :key="node.rel_path">
            <td class="strmforge-pending__check">
              <input
                type="checkbox"
                :checked="selected.has(node.rel_path)"
                @change="toggle(node.rel_path)"
              />
            </td>
            <td class="strmforge-path" :title="node.rel_path">{{ node.rel_path }}</td>
            <td>{{ node.name }}</td>
            <td>{{ formatSize(node.size) }}</td>
            <td>{{ node.modified_time ? formatTime(node.modified_time) : "—" }}</td>
            <td>{{ node.deleted_at ? formatTime(node.deleted_at) : "—" }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.strmforge-pending__bar {
  display: flex;
  gap: 12px;
  align-items: center;
  justify-content: space-between;
  padding-bottom: 8px;
}

.strmforge-pending__actions {
  display: flex;
  gap: 8px;
}

.strmforge-pending__hint {
  margin: 0 0 12px;
  color: var(--admin-sub, #6b7280);
  font-size: 13px;
  line-height: 1.6;
}

.strmforge-pending__check {
  width: 40px;
  text-align: center;
}

/* 路径可能很长，限定宽度后省略，完整内容靠 title 提示。 */
.strmforge-path {
  max-width: 460px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>