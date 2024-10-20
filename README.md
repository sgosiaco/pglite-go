# pglite-go

Testing out the wasi build shared [here](https://github.com/electric-sql/pglite/issues/89#issuecomment-2418437346)

[main.go](./main.go) logic is based on the python example included in the link above.

Socketfile usage/impl is still TBD, but for now this poc works as a stdin REPL using [wazero](https://github.com/tetratelabs/wazero) as the runtime.