-- STRM 映射的目录快照：只存文件节点，空目录不入库。
-- rel_path（相对扫描根、以 / 分隔）是主键，改名与移动自然表现为「删除 + 新增」。
-- is_active=0 是墓碑：网盘偶发读不全时先标记，等用户确认后再物理删除。
CREATE TABLE strmforge_nodes (
    -- 所属任务。
    task_id       TEXT    NOT NULL REFERENCES strmforge_tasks(id) ON DELETE CASCADE,

    -- 相对扫描根的完整路径，例如 "剧集/Silo/Silo - S01E01.mkv"。
    -- 主键的一部分；文件改名或移动会换掉这个值，因此在 diff 中表现为「删除 + 新增」。
    rel_path      TEXT    NOT NULL,

    -- 所在目录的 rel_path，根目录下的文件为空串。例如上例为 "剧集/Silo"。
    parent_path   TEXT    NOT NULL DEFAULT '',

    -- 文件名，不含目录。例如上例为 "Silo - S01E01.mkv"。
    name          TEXT    NOT NULL,

    -- 文件大小，字节。localfs 与各网盘都能给出；取不到时为 0。
    size          INTEGER NOT NULL DEFAULT 0,

    -- 修改时间，Unix 秒。已统一截断到秒，避免纳秒精度导致每次扫描都判为「已变更」。
    modified_time INTEGER NOT NULL DEFAULT 0,

    -- 1=本轮扫描读到；0=墓碑，本轮没读到，等用户确认。
    is_active     INTEGER NOT NULL DEFAULT 1,

    -- 最近一次扫描到该文件的 Unix 秒。
    last_seen_at  INTEGER NOT NULL,

    -- 被打上墓碑的时间，Unix 秒；未删除时为 NULL。
    deleted_at    INTEGER,

    PRIMARY KEY (task_id, rel_path)
);

CREATE INDEX idx_strmforge_nodes_active  ON strmforge_nodes(task_id, is_active);
CREATE INDEX idx_strmforge_nodes_deleted ON strmforge_nodes(task_id, deleted_at) WHERE deleted_at IS NOT NULL;