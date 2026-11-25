# Data Worker 协议 v1

Go 通过标准输入发送一个 JSON 对象；字段为 `schema_version`（1）、`run_id`、
`operation`、`provider`、`as_of`（ISO 日期）、`params`（对象）。
一期 provider 固定为 `baostock`；`collect` 执行白名单查询或本地原文重放，
`protocol_check` 仅返回零记录批次，不生成行情。

标准输出是 UTF-8 JSONL，每一行必须以 LF 结束：

1. `begin`：schema/run ID、Worker 版本、依赖锁文件 SHA-256、适配器版本。
2. 零到多行 `record`：`{"type":"record","data":{...}}`。
3. `end`：`record_count` 与 `sha256`。

哈希覆盖所有 `record` 行的**原始 UTF-8 字节（包含 LF）**，不含 begin/end。
空批次哈希是 SHA-256 空字节串。end 后不得继续输出内容。
Go 拒绝版本或 run ID 不匹配、计数/哈希错误、截断、输出超限及非零退出。
日志只写 stderr；Go 为进程配置超时和取消，不向 API 回传供应商 stderr。

协议验证只验证信封，不代表行情字段和质量验证通过。后续领域校验必须完成
证券身份、交易日、价格单位、公司行动、完整率和原始制品校验后才能发布。
Worker 使用 BaoStock 0.9.4 及随包分发的完整
`stock_worker/dependencies.lock`；启动用 `protocol_check/check_dependencies` 校验版本。
开发检查工具及其传递依赖单独固定在 `requirements-dev.lock`。

## 日行情记录契约

JSONL 信封版本仍为 1，数据库模型版本 3 与信封版本分别管理。
`providers/daily_model.py` 固定全部 18 个日 K 字段，将 SDK 字符串转换为
`symbol/bar/valuation/source_record`；完整字段映射见
[日行情数据模型](../architecture/market-data-model.md)。

`bar` 的 OHLC、`previous_close` 和 `amount` 均为人民币元乘 10000 的整数，
`volume` 为股数，换手率/涨跌幅为比例；这与对外 API 的元价格表示不同。
`valuation` 包含四个 nullable 估值比率。`source_record` 保留全部原始字符串、
空字符串和未知新增字段。缺失可选数值转为 null，非法必填值拒绝转换。
该函数只归一化不复权日线；供应商 qfq 样本只保存原行供 Go 核验。

在线采集每个接口批次登记 operation、请求参数、返回字段顺序、
获取时间、错误、记录数、适配器版本及不可变原始制品哈希。因子/分红的记录
类型封装在 `source_response.records` 中。外层每个 record 对应一个查询响应；
包含完整 SDK fields/rows、complete/error_code、逐行 issues 和归一化事实。
详细白名单、检查点与重放契约见 [采集说明](baostock-collection.md)。

`daily_status` 表示来源明确停牌但 OHLC/量/额不完整的行，包含 symbol、
trade_date、trade_status、is_st、valuation 和完整 source_record，没有 bar。
Go 结合上市/退市日期写状态与估值；不完整的交易状态行仍登记 INVALID_RECORD。
