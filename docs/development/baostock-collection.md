# BaoStock 历史日行情采集

状态：2026-11-24 已实现；验证结果见 [验收记录](baostock-acceptance.md)。

Go 负责业务编排与全部 SQLite 写入；Python Worker 每批建立一个串行 SDK 会话，
输出 v1 JSONL 信封，结束后登出。SDK 为 BaoStock 0.9.4。

## 来源查询

`collect.params.queries` 由 1–8 个白名单查询组成，每个查询具有稳定
`query_id`、`operation` 和字符串参数 `params`。日行情、历史区间和当日证券集合
各自单独一批；小型查询可以合批。支持：

| operation | 参数 |
|---|---|
| stock_basic | code（空字符串表示全部主数据） |
| trade_dates | start_date、end_date |
| all_stock | day |
| daily_history | date |
| history | code、start_date、end_date、adjustflag（3 基准，2 核验样本） |
| adjust_factor | code、start_date、end_date |
| daily_adjust_factor | date |
| dividend | code、year、year_type（operate/report） |

每条 `source_response` 保存字段顺序、SDK 原字符串行、精确请求、获取时间、
来源错误、完整结束标志、标准化记录和逐行问题。无来源发布时间时保持 NULL。
标准化错误保留原行。来源成功空因子/分红是合法完成证据；错误/截断不是空集合。
`protocol_check` 无需 SDK 或网络。

## 原文与检查点

Go 验证 begin/record/end、记录数和字节哈希后，为每个查询保存独立 gzip 制品。
压缩字节 SHA-256 是内容地址。原始制品登记、snapshot、调用审计、
标准化写入和完成单元在短事务中提交。重放前验证制品长度和哈希。

003 是前向迁移；001/002 保持原样。采集上下文冻结父版本、目标锚点与请求范围；
单元按 operation/params 的确定性哈希去重，attempt 审计每次调用。
显式重试使用同一 job_id 和递增 attempt，重新构建 building 版本；
重启中断不自动重试，完成查询可复用原文。

## 复权与范围

Go 使用精确有理数计算 `none(t) × B(t)/B(anchor)`，首事件前 B=1，
最终 ROUND_HALF_UP 到万分之一元。只改变 OHLC，前收参考价、量额、
来源比例和估值保持原口径。供应商 qfq 仅用作锚点归一后的抽样核验。

逻辑版本继承股票/年份分区、完整因子/公司行动集合及各股实际声明区间。
修改整年块时复制旧数据；不把区间之间未请求的缺口算作已覆盖。
逐日完整率、因子/分红完成证据、冲突、复权核验与清单引用共同决定发布。
原子发布必须同时确认父活动版本仍然匹配并完成 job/attempt。

日 K 合同实际为 18 个字段；原计划“19”是计数错误。历史按股/年拉取，
超时或输出超限可二分缩短区间；SDK/协议错误不会触发递归拆分。
全市场批次缺接口/字段不兼容时回退单股不复权采集，再修复缺口。
默认查询间隔 200ms，每查询最多 3 次，后两次等待 1s、3s。
当前每个逻辑股票刷新完整因子，重建 qfq；日更分红查询近两年及因子事件年份，
历史补采查询上市至锚点的全部年份，两种 year_type 均保存。
本期优先正确性，没有实现因子无变化时跳过全部历史 qfq 重建的优化。
状态与估值的继承来源也进入清单原文哈希核验。
因子输入哈希绑定完整有序因子、行动记录及因子/分红原文（包括成功空查询）；
总清单哈希另覆盖全部物理分区、声明区间与来源原文。
复权样本界限传播供应商价格、前/后复权因子的末位半单位误差，
另含最终价格舍入，不使用固定百分比容差。
SDK 在退市当天可能仍返回停牌占位价格，但量/额为空：原文重放会转换为
`daily_status`，保留估值与状态，不生成日线；结合 outDate 确认退市边界。
在有效上市区间内，来源明确的同类停牌状态可计入覆盖；交易状态缺必填值仍失败。

API POST `/api/v1/market-data/generations?retry=true` 接受相同失败请求；
GET 任务返回阶段、查询单元进度、完整率、质量制品 ID。行情状态返回声明区间、
范围与 stale 信息。部分股票请求不能推进含其他股票的全局锚点；
补采更早历史保持现有锚点。只有已收盘的历史日期可以提交。

## 运行环境

运行依赖锁：`worker/stock_worker/dependencies.lock`，支持 Python 3.11–3.13。
在仓库外建立环境并使用 `STOCK_WORKER_PYTHON` 指定解释器。
全市场范围为 BaoStock 可获取的沪深 A 股，北交所是明确的来源缺口。
本次开发只运行有限在线抽样，不运行整个全市场多年代补采。
