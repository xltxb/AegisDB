# omni 精简副本

来源:https://github.com/bytebase/omni (MIT,见 `LICENSE`)
版本:`v0.0.0-20261105065730-7661e876d7b9`

只保留 `mysql/{ast,parser}` 和 `oracle/{ast,parser}` 的非测试代码,没有改动。由 `backend/go.mod` 的 `replace` 指向这里。

## 为什么不直接依赖

上游 `go.mod` 要求 Go 1.25.7,还带着测试用的依赖。直接 `require` 会把网关的这些依赖一起升上去,而解析器根本用不到它们:

| 依赖 | 现在 | 会升到 |
|---|---|---|
| `go-sql-driver/mysql` | v1.7.1 | v1.9.3 |
| `sijms/go-ora/v2` | v2.8.19 | v2.9.0 |
| `golang.org/x/crypto` | v0.31.0 | v0.48.0 |
| `golang.org/x/net` | v0.25.0 | v0.49.0 |

Go 的版本选择没法把它们压回来。这里的 `go.mod` 没有依赖,`go` 指令写的是 1.23.0(与网关相同,这几个包按 1.23 能编译),所以网关的驱动和 Go 版本都不变。

## 升级

取新版本的这四个目录,去掉 `_test.go`,覆盖过来,更新本文件的版本号,再跑 `internal/sqlparse` 的测试和语料库报告。
