# Stock Analysis Assistant

面向个人投资者的 A 股分析助手，通过可追溯的数据与分析结果，多角度识别值得进一步研究的公司。

## 当前状态

已初始化技术一期的骨架与契约：Go 模块化单体、SQLite/WAL、持久化任务、
5 个 REST API、Python JSONL Worker、CLI 和离线检查。
日行情模型已扩展为 BaoStock 全部 19 个日 K 字段、独立估值/逐日状态、
三种复权因子、分红记录及股票/年份版本清单；存储迁移和查询契约已落地。

**尚未完成行情 MVP**：BaoStock 采集/准入、交易日与证券同步、前复权、
完整率门禁、原子发布、重试和日更调度属于后续建设。当前生成 API 返回
`202`，后台将任务标记为 `failed / SOURCE_UNAVAILABLE`；空库行情状态返回
`503 / NO_ACTIVE_DATASET`，不会生成或发布模拟行情。

## 本地运行

要求 Go 1.23+、Python 3.11+；一期无需 Node/Vue。
从仓库根目录执行：

```bash
go mod download
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
当前仅允许回环 IP；数据源固定为待准入的 BaoStock。

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
make doctor
make backup
make test
make worker-check

# 安装完整检查工具到仓库外的虚拟环境
python3 -m venv /tmp/stock-tools
/tmp/stock-tools/bin/python -m pip install -r worker/requirements-dev.lock
make check PYTHON=/tmp/stock-tools/bin/python RUFF=/tmp/stock-tools/bin/ruff
```

`generate` 调用运行中的 stockd，保持服务为业务写入入口；离线恢复执行器
尚未实现。`doctor` 检查数据库完整性、WAL 和最后任务；`backup` 在线快照
数据库并复制 raw/artifacts，写入文件 SHA-256 清单，失败不保留完整备份标记。
初始化执行全部有序迁移；已有库升级与大规模补采前须先 `make backup`。
迁移 002 将成交额从分转为万分之一元，保留旧数据并扩展完整日行情模型。
备份恢复应停服务，将备份的数据库与 raw/artifacts 一起恢复，并核对清单；
自动恢复演练、引用校验和版本保留清理在后续实现。

重启时 queued 任务继续处理，running 任务及其 attempt 明确标记为失败；
building 版本标记为 rejected。相同请求复用既有 ID（含失败任务），
失败任务重试入口将在生成链路阶段补齐。

目录按业务模块组织：`internal/{securities,marketdata,jobs,delivery}`，
供应商/持久化在 `internal/integrations`，技术原语在 `internal/platform`。
不提前创建后续模型、问答、组合或前端模块。

## 项目文档

- [架构设计](docs/architecture/README.md)
- [产品架构（终版）](docs/architecture/product-architecture.md)
- [技术架构（终版）](docs/architecture/technical-architecture.md)
- [一期日行情数据模型](docs/architecture/market-data-model.md)
- [一期 API 契约](api/openapi.yaml)
- [Worker 协议 v1](docs/development/worker-protocol.md)
