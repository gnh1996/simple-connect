# UX / 性能审查纪要与落地

本文记录列表页 / 表单页 / SFTP 页若干 UX 与性能问题的审查结论与落地方式。
源码注释中以 `docs/ux-perf-review.md <编号>` 引用的条目均在此。

## 2.1 状态探测的世代号（tickGen / probeGen）

- **问题**：周期探测与手动刷新（`s`）的结果可能乱序，过期结果覆盖新结果；
  且列表不在前台（处于表单 / SFTP / SSH 会话）时旧的轮询链已进不了 `list.Update`，
  若用 `armed` 布尔标记“是否已挂链”，回列表后布尔残留会导致不再续订周期探测。
- **落地**：双世代号。
  - `tickGen` 管**续订链**：`Init` / 回到列表时自增并挂一条带该 gen 的新 tick；
    过期 gen 的 tick 直接丢弃、不续订。**禁止用 armed 布尔。**
  - `probeGen` 管**结果**：每次发起探测自增，结果带 gen 回传，过期结果丢弃。
- 见 `internal/tui/list.go`、`internal/tui/list_state_test.go`。

## 2.2 过滤与光标钳位的顺序

- **问题**：过滤输入时若先用“过滤前长度”钳位光标，再重算过滤结果，结果收窄后
  光标会越界，表现为列表无高亮。
- **落地**：每次过滤输入先 `applyFilter()`，再 `clampCursorToFiltered()`，
  最后 `ensureVisible()`（重算在前、钳位在后）。
- 见 `internal/tui/list.go`。

## 2.3 表单 View 只读

- **问题**：在 `View()` 里清错误提示，会让任意下一条 `Msg`（含 `WindowSizeMsg`，
  甚至一次重绘）把提示清掉，用户容易以为已提交。
- **落地**：`formModel.View()` 不 mutate 模型；错误保留到用户下一次按键的
  `Update` 才清除（非编辑类消息不清）。
- 见 `internal/tui/form.go`、`internal/tui/form_auth_test.go`。

## 3.1 连接反馈与忙碌复位（历史条目，现已重构）

- **问题**：SFTP 拨号期间无反馈；拨号失败未复位 `busy`，导致 `t/p/x/r` 全被
  永久挡住。
- **落地**：`Init` 置“正在连接…”；失败 / 待指纹确认分支复位。
  该状态现已由显式 `connState`（Idle/Connecting/Ready/Failed）取代，
  详见 `AGENTS.md` 的「SFTP 页状态模型」。

## 8.7 过期异步结果不得覆盖新状态

- **问题**：与 2.1 同源——手动刷新 `s` 与周期探测并发时，旧探测结果到达可能
  覆盖新结果；列表往返（重建 `tea.Program`）后轮询链需要被正确重挂。
- **落地**：同 2.1 的 `probeGen`（作废过期结果）与 `tickGen`（保证往返后
  有且仅有一条有效续订链）。SFTP 覆盖检测的同类问题用 `overwriteSeq` 作废
  迟到结果（见 `AGENTS.md`「传输前扫描」）。
