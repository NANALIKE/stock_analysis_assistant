# Stock Analysis Assistant

面向个人投资者的 A 股分析助手，通过可追溯的数据与分析结果，多角度识别值得进一步研究的公司。

## 当前状态

已实现 BaoStock 历史补采与手动日更：证券/交易日同步、全部 18 个日 K
字段、估值/逐日状态、完整因子与分红原文、本地 qfq、分区继承、
99.5% 完整率门禁、原子发布及显式失败重试。
Go 负责串行编排和 SQLite 写入，Python JSONL Worker 调用锁定的 SDK。
范围为沪深 A 股（包含历史退市股），北交所列为来源缺口。
在线验证使用有限样本，未执行全市场多年代下载；详情见
[采集说明](docs/development/baostock-collection.md)。

## 本地运行

要求 Go 1.23+、Python 3.11+；一期无需 Node/Vue。
从仓库根目录执行：

```bash
go mod download
python3 -m venv /tmp/stock-baostock-runtime
/tmp/stock-baostock-runtime/bin/python -m pip install -r worker/stock_worker/dependencies.lock
export STOCK_WORKER_PYTHON=/tmp/stock-baostock-runtime/bin/python
make init
make run
```

默认监听 `127.0.0.1:8080`。首次启动自动执行内嵌迁移，并创建
`var/stock.db`、`var/raw`、`var/artifacts`、`var/backups`。
修改配置可复制 `configs/config.example.yaml` 为 `configs/config.local.yaml`，
然后使用 `make run CONFIG=configs/config.local.yaml`。
相对路径均相对于命令的工作目录。

YAML 字符串配置支持对应的 `STOCK_*` 环境变量覆盖，具体映射见
`internal/platform/config/config.go`。示例：`STOCK_SERVER_ADDRESS=127.0.0.1:8090`。
当前仅允许回环 IP；数据源固定为 BaoStock。启动会校验 Worker 依赖锁。

```bash
# 创建任务；用返回的 status_url 查询结果
curl -s http://127.0.0.1:8080/api/v1/market-data/generations \
  -H 'Content-Type: application/json' -d @testdata/generation.json

curl -s 'http://127.0.0.1:8080/api/v1/instruments?query=000001'
curl -s http://127.0.0.1:8080/api/v1/market-data/status
```

## 命令与验证

```bash
make build
bin/stockctl generate -request testdata/generation.json
# 有限历史样本；省略 symbols 时选择全市场
bin/stockctl backfill -from 2000-01-04 -to 2000-01-10 -symbols 600000.SH
# 失败任务用相同请求显式重试
bin/stockctl backfill -from 2000-01-04 -to 2000-01-10 -symbols 600000.SH -retry
make doctor
make backup
make test
make worker-check

# 安装完整检查工具到仓库外的虚拟环境
python3 -m venv /tmp/stock-tools
/tmp/stock-tools/bin/python -m pip install -r worker/requirements-dev.lock
make check PYTHON=/tmp/stock-tools/bin/python RUFF=/tmp/stock-tools/bin/ruff
```

`generate/backfill` 调用运行中的 stockd，保持服务为业务写入入口。
日更使用 `mode=daily` 与显式 `trade_date`，当前没有自动日更调度。
`doctor` 检查数据库完整性、WAL 和最后任务；`backup` 在线快照
数据库并复制 raw/artifacts，写入文件 SHA-256 清单，失败不保留完整备份标记。
初始化执行全部有序迁移；已有库升级与大规模补采前须先 `make backup`。
迁移 002 将成交额从分转为万分之一元；003 增加采集上下文、检查点、审计和声明区间。
备份恢复应停服务，将备份的数据库与 raw/artifacts 一起恢复，并核对清单；
自动恢复演练、引用校验和版本保留清理在后续实现。

重启时 queued 任务继续处理，running 任务及其 attempt 明确标记为失败；
building 版本标记为 rejected。相同请求复用既有 ID（含失败任务），
用相同请求加 `?retry=true` 或 CLI `-retry` 重试，保留 job_id、递增 attempt；
已完成的查询验证原文哈希并重新归一化，无需再次联网。

目录按业务模块组织：`internal/{securities,marketdata,jobs,delivery}`，
供应商/持久化在 `internal/integrations`，技术原语在 `internal/platform`。
不提前创建后续模型、问答、组合或前端模块。

## 项目文档

- [产品需求与能力索引](docs/product/README.md)
- [架构设计](docs/architecture/README.md)
- [产品架构（终版）](docs/architecture/product-architecture.md)
- [技术架构（终版）](docs/architecture/technical-architecture.md)
- [一期日行情数据模型](docs/architecture/market-data-model.md)
- [一期 API 契约](api/openapi.yaml)
- [Worker 协议 v1](docs/development/worker-protocol.md)
