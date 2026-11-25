# BaoStock 历史采集验收记录

日期：2026-11-24。范围：已批准的历史补采、手动日更与有限真实抽样。
全市场多年代任务没有启动，北交所明确不受本适配器支持。

实现包含证券/日历、18 字段不复权日线、估值、逐日状态、完整因子和分红，
原文制品/审计、可重放检查点、本地 qfq、逐日及总体 99.5% 门禁、
股票/年份继承、原子发布、失败重试和 API/CLI 进度查询。

## 离线验证

`make check PYTHON=/tmp/stock-init-tools/bin/python RUFF=/tmp/stock-init-tools/bin/ruff`
通过 Go/Python 测试、go vet、Ruff lint/format、OpenAPI 校验和 diff 空白检查；
`make build` 构建 stockd、stockctl。

单测范围：精确复权和价格舍入、供应商锚点归一、价格/因子精度传播、
除权参考价、完整率分母、真实原文、发布/继承/失败隔离/续采、Worker 会话与协议。
正常/边界用例包括空因子、历史锚点、溢出、非法字段、负估值、四位成交额、
跨年/同年继承、声明区间缺口、SDK 缺导出、分页结束错误、stdout 隔离和登出。
全市场日批次和 SDK 缺接口时的单股回退分别执行，确认生成的 qfq、逐日状态与
完整率相同；测试范围使用固定来源，实际全市场接口另做在线单日探测。
重试用例确认已完成查询走原文重放，job_id 不变、attempt 递增；
历史修复用例确认全局锚点不倒退、旧年块完整复制、核验制品按内容复用。

缺陷分析：真实事件回归稳定复现遗漏供应商 fore 因子精度导致的误拒绝。
修复前 `TestRealSupplierEventPrecision` 失败；修复后真实 11 日 OHLC 全部通过，
人工改变供应商价格仍失败。修复沿用原定末位半单位区间传播规则。
结果日志为 `/tmp/stock-event-regression-before.log`、
`/tmp/stock-event-regression-after.log`，最终检查日志为
`/tmp/stock-collection-acceptance-check.log`。单测工作流的报告 flush 已执行。

在线 Worker 无响应异常进一步复现重试结束路径的错误变量遮蔽，
导致最终错误值为空并 panic。新增 `TestTransportFailureEndsJobWithoutPanic`，
修复后任务正常以 SOURCE_UNAVAILABLE 失败、保留调用审计；重启后显式 retry
复用检查点继续执行。修复前后日志为 `/tmp/stock-transport-regression-before.log`
和 `/tmp/stock-transport-regression-after.log`。

退市原文进一步复现缺量额的停牌占位行被当作非法日线的问题。
新增真实 Python 回归和 Go 状态/估值持久化回归；修复后保留 daily_status、
估值与原始字段，不生成价格，退市日期不计入有效上市日分母。
`/tmp/stock-delisted-regression-before.log` 保留修复前错误，
`/tmp/stock-delisted-go-regression.log` 记录持久化回归通过。

## 真实在线样本

SDK 0.9.4、pandas 2.2.3 及传递依赖固定在运行锁中；
解释器 `/tmp/stock-baostock-runtime/bin/python`，各样本使用独立数据库/raw/artifacts，
查询串行执行。Worker stdout 仅包含 v1 JSONL，供应商登录日志写 stderr。

| 场景 | 请求/证据 | 结果 |
|---|---|---|
| 2000 年老股 | 600000.SH，2000-01-04 至 2000-01-10 | 成功，5/5，完整率 100% |
| 首除权事件 | 配对原文 2000-06-29 至 2000-07-13 | 11 日真实回归；因子与除权参考价核验 |
| 历史锚点继承 | 600000.SH，2001-08-24 至 2001-08-30 | attempt=2 成功，累计 10/10；93 项复权核验 |
| 多事件与同年日更 | 600000.SH，2003-08-25 至 08-29；09-01 日更 | 成功，累计 15/15 → 16/16；183 项复权核验 |
| 新股与上市前状态 | 300750.SZ，2018-06-06 至 06-14，上市日 06-11 | 成功，4/4；上市前仅状态，无伪造日线 |
| 中断与显式续采 | 300750.SZ，2018-06-15 日更停服务，再以相同请求 retry | 同 ID，attempt 1 → 2；累计 5/5，已完成主数据走原文重放 |
| 退市边界 | 688086.SH，2023-07-03 至 2023-07-10 | 原文重放后成功，4/4，130 项核验；07-07、07-10 仅退市状态 |
| 单日全市场接口 | 2000-01-04，证券集合与日批次 | 926/926 个 A 股身份一致，18 字段且 adjustflag=3；另排除 14 条非 A 股记录 |

首次跨事件构建被门禁拒绝时，旧活动版本保持可读，失败原文和报告完整保存。
供应商 qfq 样本只作核验；正式 qfq 均由本地 none 与累计后复权因子计算。

三个样本库均通过 integrity_check、foreign_key_check；全部登记制品均校验
压缩字节长度、SHA-256、解压和 JSON。每库只有一个 active，无 running 遗留任务。
在线来源接口探测只核对单日原文，不发布全市场完整版本。
完整任务、版本、完整率和清单哈希见 [在线汇总](../../var/baostock-acceptance.json)；
[全市场探测](../../var/online-market-probe/summary.json) 保存接口字段与记录数。
原始响应、质量报告和查询结果保留在 `var/online-sample`、`var/online-new`、
`var/online-delisted`，未复制到默认库。用于核验的临时服务均已停止。

## 数据库升级

已核验原备份 `var/backups/backup_eeef3bf24164ba1f242fad3daca73d0c/manifest.json`。
升级前创建新备份 `var/backups/backup_04e0149cc4c13b9d1d4443b1b7eb3b7f`，
随后默认库应用 003；`doctor` 返回 integrity=ok、WAL、foreign_keys=1。
001/002 未改动。在线数据保留在隔离样本库，默认库没有导入抽样冒充全市场。

## 使用与验证边界

先按 [README](../../README.md) 安装运行锁并启动 stockd，然后执行：

```bash
# 默认 from=2000-01-01；省略 symbols 选择全部沪深 A 股
bin/stockctl backfill -to 2026-11-23
# 指定有限样本
bin/stockctl backfill -from 2000-01-04 -to 2000-01-10 -symbols 600000.SH
# 失败后用完全相同请求显式续采
bin/stockctl backfill -from 2000-01-04 -to 2000-01-10 -symbols 600000.SH -retry
```

未验证全市场多年代完成时间、全部停牌跨事件、免费源 18:00 更新承诺、
所有历史修订情形、生产级备份恢复演练。自动日更调度、第二数据源、
历史版本清理与不变 qfq 跳过重建未实施。
今天获取的历史数据不等于当年可用信息，不承诺 point-in-time 回测。
