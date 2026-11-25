这里保存固定、离线契约样本，不代表已通过数据源准入。

- `generation.json`：API/CLI 请求样例。
- `worker-request.json`：零记录协议自检，验证进程和 JSONL，不发布行情。
- `baostock-daily.json`：官方 API 文档的一条固定日线，覆盖全部 18 个字段，
  含原文链接；验证字段留存、四位成交额、换手/涨跌幅与负估值。

- `baostock-event-online.json`：2026-11-24 从 SDK 0.9.4 实际采集的
  600000.SH 因子与 2000-06-29 至 2000-07-13 的 none/qfq 配对原文，
  用于跨除权事件和六位因子精度回归。包含获取时间和完整请求。
- `baostock-delisted-online.json`：688086.SH 退市窗口实际原文；
  2023-07-07 的停牌行成交量/额为空，验证保留状态和估值、不造日线。

其余在线样本与质量报告保存在 `var/online-*`，验证范围见
[采集验收](../docs/development/baostock-acceptance.md)。
单测对固定行修改数值只用于精度和错误边界测试，不作为真实行情或来源准入证据。
