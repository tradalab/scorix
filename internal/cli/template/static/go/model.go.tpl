package {{ .Package }}

import scorixsqlx "github.com/tradalab/scorix/module/sqlx"

var _ {{ .Table.GoName }}Model = (*custom{{ .Table.GoName }}Model)(nil)

// {{ .Table.GoName }}Model is the repository contract. Add custom methods
// here and implement them on custom{{ .Table.GoName }}Model.
type (
	{{ .Table.GoName }}Model interface {
		{{ lowerFirst .Table.GoName }}Model
	}

	custom{{ .Table.GoName }}Model struct {
		*default{{ .Table.GoName }}Model
	}
)

// New{{ .Table.GoName }}Model takes `sqlxMod.Conn` (no parens) — bound method
// value for lazy resolution and WithTx propagation.
func New{{ .Table.GoName }}Model(conn func() scorixsqlx.Conn) {{ .Table.GoName }}Model {
	return &custom{{ .Table.GoName }}Model{
		default{{ .Table.GoName }}Model: newDefault{{ .Table.GoName }}Model(conn),
	}
}
