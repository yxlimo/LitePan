-- STRM 映射任务：绑定一个网盘账号与一个扫描目录，只记录不移动源文件。
CREATE TABLE strmforge_tasks (
    -- 任务主键，UUID。
    id                  TEXT PRIMARY KEY,

    -- 任务名，前端限制 10 个字符。
    task_name           TEXT NOT NULL,

    -- 网盘账号。账号被删除时连带删除本任务。
    account_id          INTEGER NOT NULL REFERENCES cloud_accounts(id) ON DELETE CASCADE,

    -- 扫描目录的展示路径，例如 "/剧集/Silo"。只用于展示与回显。
    source_directory    TEXT NOT NULL DEFAULT '',

    -- 扫描目录在驱动里的标识。网盘为 file_id；localfs 为空串（根目录 id 为空）。
    -- localfs 的账号 id 就是绝对路径，所以这一列只对网盘有意义。
    source_directory_id TEXT NOT NULL DEFAULT '',

    -- 扫描目录往下最多递归几层，由创建任务时填写，默认 5，最大 10。
    max_depth           INTEGER NOT NULL DEFAULT 5,

    -- 任务状态：idle（空闲）/ scanning（扫描中）。
    status              TEXT NOT NULL DEFAULT 'idle',

    -- 最近一次扫描完成时间，Unix 秒。
    last_scan_at        INTEGER,

    -- 最近一次扫描结果的 JSON 文本。列是 TEXT 而非 JSON：
    -- SQLite 没有原生 JSON 类型，声明 JSON 会得到 NUMERIC affinity。
    last_scan_result    TEXT NOT NULL DEFAULT '',

    -- 记录创建与最后修改时间。
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_strmforge_tasks_account ON strmforge_tasks(account_id);
CREATE INDEX idx_strmforge_tasks_status ON strmforge_tasks(status);