<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import { getApiErrorMessage } from "@/api/client";
import {
  fetchStrmForgeNodes,
  fetchStrmForgeTasks,
  type StrmForgeNode,
  type StrmForgeTask,
} from "@/api/strmForge";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import AppButton from "@/components/base/AppButton.vue";
import AppInput from "@/components/base/AppInput.vue";
import AppSelect from "@/components/base/AppSelect.vue";
import { toast } from "@/composables/useToast";
import { formatTime } from "@/utils/format";
import "@/styles/admin-shared.css";
import "@/styles/admin-table.css";

const PAGE_SIZE = 100;

// 父级可指定初始任务（例如从任务列表点进来时）。
const props = defineProps<{ taskId?: string | null }>();

const tasks = ref<StrmForgeTask[]>([]);
const taskID = ref<string | null>(props.taskId ?? null);
const nodes = ref<StrmForgeNode[]>([]);
const total = ref(0);
const offset = ref(0);
const keyword = ref("");
const loading = ref(false);
const listReady = ref(false);

const taskOptions = computed(() =>
  tasks.value.map((task) => ({ value: task.id, label: task.task_name })),
);

const currentTask = computed(() => tasks.value.find((task) => task.id === taskID.value) ?? null);
const hasMore = computed(() => offset.value + nodes.value.length < total.value);
const pageFrom = computed(() => (total.value === 0 ? 0 : offset.value + 1));
const pageTo = computed(() => offset.value + nodes.value.length);

async function loadTasks() {
  try {
    tasks.value = await fetchStrmForgeTasks();
    if (!taskID.value && tasks.value.length) {
      taskID.value = tasks.value[0].id;
      return;
    }
    // 当前任务已被删除时回退到第一个。
    if (taskID.value && !tasks.value.some((task) => task.id === taskID.value)) {
      taskID.value = tasks.value[0]?.id ?? null;
    }
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载任务失败"));
  }
}

async function loadNodes() {
  if (!taskID.value) {
    nodes.value = [];
    total.value = 0;
    return;
  }
  loading.value = true;
  try {
    const page = await fetchStrmForgeNodes(taskID.value, {
      q: keyword.value.trim() || undefined,
      limit: PAGE_SIZE,
      offset: offset.value,
    });
    nodes.value = page.nodes ?? [];
    total.value = page.total;
  } catch (e) {
    toast.error(getApiErrorMessage(e, "加载扫描结果失败"));
  } finally {
    loading.value = false;
    listReady.value = true;
  }
}

function resetAndLoad() {
  offset.value = 0;
  void loadNodes();
}

function prevPage() {
  offset.value = Math.max(0, offset.value - PAGE_SIZE);
  void loadNodes();
}

function nextPage() {
  if (!hasMore.value) return;
  offset.value += PAGE_SIZE;
  void loadNodes();
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
  offset.value = 0;
  void loadNodes();
});

onMounted(async () => {
  await loadTasks();
  if (taskID.value) await loadNodes();
  else listReady.value = true;
});

defineExpose({ loadTasks });
</script>

<template>
  <div class="strmforge-nodes">
    <div class="strmforge-nodes__bar">
      <AppSelect
        v-model="taskID"
        :options="taskOptions"
        :disabled="!tasks.length"
        placeholder="选择任务"
      />
      <AppInput
        v-model="keyword"
        placeholder="按路径搜索"
        class="strmforge-nodes__search"
        @keyup.enter="resetAndLoad"
      />
      <AppButton type="button" :disabled="loading" @click="resetAndLoad">搜索</AppButton>
    </div>

    <AdminEmptyState
      v-if="listReady && !tasks.length"
      icon="hand-folder"
      title="还没有 STRM 映射任务"
      description="先在任务列表里添加任务并扫描目录。"
    />

    <AdminEmptyState
      v-else-if="listReady && !loading && !nodes.length"
      icon="hand-folder"
      :title="keyword.trim() ? '没有匹配的文件' : '还没有扫描结果'"
      :description="
        keyword.trim()
          ? '换个关键字再试试，或清空搜索查看全部。'
          : '对任务执行一次扫描后，这里会按文件列出目录结构。'
      "
    />

    <div v-else class="admin-panel-table-wrap">
      <table class="admin-table">
        <thead>
          <tr>
            <th>文件名</th>
            <th>相对路径</th>
            <th>大小</th>
            <th>修改时间</th>
            <th>最近扫描</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="node in nodes" :key="node.rel_path">
            <td :title="node.name">{{ node.name }}</td>
            <td class="strmforge-path" :title="node.rel_path">{{ node.rel_path }}</td>
            <td>{{ formatSize(node.size) }}</td>
            <td>{{ node.modified_time ? formatTime(node.modified_time) : "—" }}</td>
            <td>{{ node.last_seen_at ? formatTime(node.last_seen_at) : "—" }}</td>
          </tr>
        </tbody>
      </table>

      <div class="strmforge-nodes__pager">
        <span class="admin-account-sub">
          共 {{ total }} 个文件{{ currentTask ? ` · ${currentTask.task_name}` : "" }}
          <template v-if="total">，当前 {{ pageFrom }}–{{ pageTo }}</template>
        </span>
        <span class="strmforge-nodes__pager-actions">
          <AppButton type="button" :disabled="offset === 0 || loading" @click="prevPage">
            上一页
          </AppButton>
          <AppButton type="button" :disabled="!hasMore || loading" @click="nextPage">
            下一页
          </AppButton>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.strmforge-nodes__bar {
  display: flex;
  gap: 8px;
  align-items: center;
  padding-bottom: 12px;
}

/* 路径可能很长，限定宽度后省略，完整内容靠 title 提示。 */
.strmforge-path {
  max-width: 460px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.strmforge-nodes__search {
  flex: 1;
  min-width: 160px;
}

.strmforge-nodes__pager {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
  padding: 12px 4px;
}

.strmforge-nodes__pager-actions {
  display: flex;
  gap: 8px;
}
</style>