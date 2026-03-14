# module/template

Skeleton template for a scorix module. Copy and modify to create new modules.

## Usage

```bash
cp -r module/template module/mymodule
```

Then:
1. Rename `package template` → `package mymodule`
2. Update module path in `go.mod`
3. Rename `TemplateModule` → `MyModule`, update `Name()` / `Version()`
4. Add to `go.work`
5. Enable in `app.yaml`

## Config

```yaml
modules:
  mymodule:
    enabled: true
    # your config fields here
```

## Registering

```go
app.Modules().Register(mymodule.New())
```

## Exposing IPC Handlers

```go
func (m *MyModule) OnLoad(ctx *module.Context) error {
    module.Expose(m, "Hello", ctx.IPC)
    return nil
}

// JS: scorix.invoke("mymodule:Hello", { name: "World" })
func (m *MyModule) Hello(ctx context.Context, req HelloRequest) (*HelloResponse, error) {
    return &HelloResponse{Message: "Hello, " + req.Name}, nil
}
```

## Module Lifecycle

```
Register → Load → Start → [running] → Stop → Unload
```
| Hook       | Purpose                              |
|------------|--------------------------------------|
| `OnLoad`   | Open connections, register IPC       |
| `OnStart`  | Start goroutines, run seed functions |
| `OnStop`   | Graceful shutdown (reverse order)    |
| `OnUnload` | Release remaining state              |
